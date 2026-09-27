use super::TunnelView;
use std::collections::{HashMap, HashSet};
use std::path::PathBuf;
use std::sync::{mpsc, Arc, Condvar, Mutex};
use std::time::{Duration, Instant};

const WORKERS: usize = 4;
const QUEUED_READS: usize = 64;
const SELECTED_TTL: Duration = Duration::from_secs(1);
const BACKGROUND_TTL: Duration = Duration::from_secs(10);
const STALE_AFTER: Duration = Duration::from_secs(15);
const SNAPSHOT_WAIT: Duration = Duration::from_millis(500);

struct Job {
    generation: u64,
    name: String,
    path: PathBuf,
    quick: PathBuf,
}
struct Cached {
    at: Instant,
    view: TunnelView,
}
#[derive(Default)]
struct State {
    generation: u64,
    entries: HashMap<String, Cached>,
    pending: HashSet<(u64, String)>,
}
#[derive(Default)]
struct Shared {
    state: Mutex<State>,
    changed: Condvar,
}

pub(super) struct StatusCache {
    shared: Arc<Shared>,
    jobs: mpsc::SyncSender<Job>,
}

impl Default for StatusCache {
    fn default() -> Self {
        Self::new(super::read_tunnel_status)
    }
}

impl StatusCache {
    fn new(
        reader: impl Fn(String, PathBuf, PathBuf) -> TunnelView + Send + Sync + 'static,
    ) -> Self {
        let shared = Arc::new(Shared::default());
        let (jobs, receiver) = mpsc::sync_channel::<Job>(QUEUED_READS);
        let receiver = Arc::new(Mutex::new(receiver));
        let reader = Arc::new(reader);
        for _ in 0..WORKERS {
            let shared = shared.clone();
            let receiver = receiver.clone();
            let reader = reader.clone();
            std::thread::spawn(move || loop {
                let job = match receiver.lock().unwrap().recv() {
                    Ok(job) => job,
                    Err(_) => break,
                };
                let key = (job.generation, job.name.clone());
                // Mutations invalidate queued and in-flight observations.
                if shared.state.lock().unwrap().generation != job.generation {
                    shared.state.lock().unwrap().pending.remove(&key);
                    shared.changed.notify_all();
                    continue;
                }
                let view = reader(job.name.clone(), job.path, job.quick);
                let mut state = shared.state.lock().unwrap();
                state.pending.remove(&key);
                if state.generation == job.generation {
                    state.entries.insert(
                        job.name,
                        Cached {
                            at: Instant::now(),
                            view,
                        },
                    );
                }
                shared.changed.notify_all();
            });
        }
        Self { shared, jobs }
    }

    pub(super) fn invalidate(&self) {
        let mut state = self.shared.state.lock().unwrap();
        state.generation = state.generation.wrapping_add(1);
        state.entries.clear();
        self.shared.changed.notify_all();
    }

    pub(super) fn collect(
        &self,
        mut profiles: Vec<(String, PathBuf)>,
        quick: PathBuf,
        selected: Option<&str>,
        force: bool,
    ) -> Vec<TunnelView> {
        // Cold reads and explicit refreshes enqueue the selected tunnel first.
        profiles.sort_by_key(|(name, _)| (Some(name.as_str()) != selected, name.clone()));
        let mut state = self.shared.state.lock().unwrap();
        state
            .entries
            .retain(|name, _| profiles.iter().any(|(profile, _)| profile == name));
        let generation = state.generation;
        for (name, path) in &profiles {
            let ttl = if Some(name.as_str()) == selected {
                SELECTED_TTL
            } else {
                BACKGROUND_TTL
            };
            if !force
                && state
                    .entries
                    .get(name)
                    .is_some_and(|entry| entry.at.elapsed() < ttl)
            {
                continue;
            }
            let key = (generation, name.clone());
            if state.pending.insert(key.clone()) {
                let job = Job {
                    generation,
                    name: name.clone(),
                    path: path.clone(),
                    quick: quick.clone(),
                };
                if self.jobs.try_send(job).is_err() {
                    state.pending.remove(&key);
                }
            }
        }
        let deadline = Instant::now() + SNAPSHOT_WAIT;
        while state.generation == generation
            && profiles
                .iter()
                .any(|(name, _)| state.pending.contains(&(generation, name.clone())))
        {
            let remaining = deadline.saturating_duration_since(Instant::now());
            if remaining.is_zero() {
                break;
            }
            let (next, timeout) = self.shared.changed.wait_timeout(state, remaining).unwrap();
            state = next;
            if timeout.timed_out() {
                break;
            }
        }
        profiles.sort_by(|left, right| left.0.cmp(&right.0));
        profiles
            .into_iter()
            .map(|(name, path)| {
                if let Some(entry) = state.entries.get(&name) {
                    let mut view = entry.view.clone();
                    if entry.at.elapsed() > STALE_AFTER {
                        view.status_state = "unknown".into();
                        view.status_code = Some("stale_status".into());
                        view.status_detail = Some(
                            "Status is stale; the tunnel may still be running. Refresh to retry."
                                .into(),
                        );
                    }
                    return view;
                }
                let mut view = super::tunnel_status_view(
                    name,
                    path,
                    Err("Waiting for tunnel status; other tunnels can still be managed.".into()),
                );
                view.status_code = Some("status_pending".into());
                view
            })
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};

    fn view(name: String, path: PathBuf) -> TunnelView {
        super::super::tunnel_status_view(
            name,
            path,
            Ok(super::super::DesktopStatusReport {
                protocol_version: 1,
                state: "inactive".into(),
                sampled_at: 1,
                status: None,
                error: None,
            }),
        )
    }

    #[test]
    fn cached_reads_do_not_spawn_another_process() {
        let calls = Arc::new(AtomicUsize::new(0));
        let count = calls.clone();
        let cache = StatusCache::new(move |name, path, _| {
            count.fetch_add(1, Ordering::SeqCst);
            view(name, path)
        });
        let profiles = vec![("wg0".into(), "wg0.conf".into())];
        assert_eq!(
            cache.collect(profiles.clone(), "quick".into(), Some("wg0"), false)[0].status_state,
            "inactive"
        );
        cache.collect(profiles.clone(), "quick".into(), Some("wg0"), false);
        assert_eq!(calls.load(Ordering::SeqCst), 1);
        cache.collect(profiles, "quick".into(), Some("wg0"), true);
        assert_eq!(calls.load(Ordering::SeqCst), 2);
    }

    #[test]
    fn slow_tunnel_does_not_hide_completed_observations() {
        let (release, wait) = mpsc::channel();
        let wait = Mutex::new(wait);
        let cache = StatusCache::new(move |name, path, _| {
            if name == "slow" {
                wait.lock()
                    .unwrap()
                    .recv_timeout(Duration::from_secs(5))
                    .unwrap();
            }
            view(name, path)
        });
        let profiles = vec![
            ("slow".into(), "slow.conf".into()),
            ("fast".into(), "fast.conf".into()),
        ];
        let views = cache.collect(profiles, "quick".into(), Some("fast"), false);
        assert_eq!(views[0].status_state, "inactive");
        assert_eq!(views[1].status_code.as_deref(), Some("status_pending"));
        release.send(()).unwrap();
    }

    #[test]
    fn invalidation_discards_in_flight_results() {
        let (started, ready) = mpsc::channel();
        let (release, wait) = mpsc::channel();
        let wait = Mutex::new(wait);
        let cache = Arc::new(StatusCache::new(move |name, path, _| {
            started.send(()).unwrap();
            wait.lock()
                .unwrap()
                .recv_timeout(Duration::from_secs(5))
                .unwrap();
            view(name, path)
        }));
        let worker_cache = cache.clone();
        let worker = std::thread::spawn(move || {
            worker_cache.collect(
                vec![("wg0".into(), "wg0.conf".into())],
                "quick".into(),
                None,
                false,
            )
        });
        ready.recv_timeout(Duration::from_secs(5)).unwrap();
        cache.invalidate();
        release.send(()).unwrap();
        assert_eq!(worker.join().unwrap()[0].status_state, "unknown");
        assert!(cache.shared.state.lock().unwrap().entries.is_empty());
    }
}
