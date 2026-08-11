use serde::Serialize;
use serde_json::Value;
use std::ffi::{OsStr, OsString};
use std::fs;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::sync::Mutex;
use std::time::{Duration, Instant};
use tauri::path::BaseDirectory;
use tauri::{AppHandle, Manager, State};

#[cfg(target_os = "windows")]
use tauri::{menu::MenuBuilder, tray::TrayIconBuilder, tray::TrayIconEvent, WindowEvent};

#[derive(Default)]
struct BackendState {
    versions: Mutex<Option<(String, String)>>,
}

#[derive(Clone)]
struct NativePaths {
    core: PathBuf,
    quick: PathBuf,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct BackendInfo {
    platform: &'static str,
    arch: &'static str,
    config_directory: String,
    supported: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    core_version: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    quick_version: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct TunnelView {
    name: String,
    config_path: String,
    running: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    status: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    status_detail: Option<String>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct DesktopSnapshot {
    backend: BackendInfo,
    tunnels: Vec<TunnelView>,
    refreshed_at: String,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct ImportResult {
    canceled: bool,
    imported_name: String,
    snapshot: DesktopSnapshot,
}

#[derive(Serialize)]
struct DesktopSmokeSettings {
    mode: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    source: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    name: Option<String>,
}

fn platform_name() -> &'static str {
    if cfg!(target_os = "windows") {
        "win32"
    } else if cfg!(target_os = "linux") {
        "linux"
    } else {
        std::env::consts::OS
    }
}

fn architecture_name() -> &'static str {
    match std::env::consts::ARCH {
        "x86_64" => "x64",
        "aarch64" => "arm64",
        other => other,
    }
}

fn config_directory() -> PathBuf {
    if let Some(path) = std::env::var_os("WG_QUIC_CONFIG_DIR") {
        return PathBuf::from(path);
    }
    if cfg!(target_os = "windows") {
        let root = std::env::var_os("ProgramData")
            .map(PathBuf::from)
            .unwrap_or_else(|| PathBuf::from(r"C:\ProgramData"));
        root.join("wg-quic").join("interfaces")
    } else {
        PathBuf::from("/etc/wg-quic")
    }
}

fn executable_name(name: &str) -> String {
    if cfg!(target_os = "windows") {
        format!("{name}.exe")
    } else {
        name.to_string()
    }
}

fn native_paths(app: &AppHandle) -> Result<NativePaths, String> {
    let resolve = |name: &str| -> Result<PathBuf, String> {
        let file_name = executable_name(name);
        let bundled = app
            .path()
            .resolve(format!("bin/{file_name}"), BaseDirectory::Resource)
            .map_err(|error| format!("resolve bundled {name}: {error}"))?;
        if bundled.is_file() {
            return Ok(bundled);
        }
        #[cfg(debug_assertions)]
        {
            let development = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("..")
                .join("resources")
                .join("bin")
                .join(file_name);
            if development.is_file() {
                return Ok(development);
            }
        }
        Err(format!(
            "bundled {name} executable is missing at {}",
            bundled.display()
        ))
    };
    Ok(NativePaths {
        core: resolve("wg-quic")?,
        quick: resolve("wg-quic-quick")?,
    })
}

#[cfg(target_os = "windows")]
fn hide_child_window(command: &mut Command) {
    use std::os::windows::process::CommandExt;
    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    command.creation_flags(CREATE_NO_WINDOW);
}

#[cfg(not(target_os = "windows"))]
fn hide_child_window(_: &mut Command) {}

const MAX_COMMAND_OUTPUT: u64 = 2 * 1024 * 1024;

fn read_command_pipe(mut pipe: impl Read) -> Result<Vec<u8>, String> {
    let mut output = Vec::new();
    pipe.by_ref()
        .take(MAX_COMMAND_OUTPUT + 1)
        .read_to_end(&mut output)
        .map_err(|error| format!("read native command output: {error}"))?;
    if output.len() as u64 > MAX_COMMAND_OUTPUT {
        return Err(format!(
            "native command output exceeded {} bytes",
            MAX_COMMAND_OUTPUT
        ));
    }
    Ok(output)
}

fn run_output_with_timeout(
    program: &Path,
    arguments: &[OsString],
    timeout: Duration,
) -> Result<String, String> {
    let mut command = Command::new(program);
    command
        .args(arguments)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    hide_child_window(&mut command);
    let mut child = command
        .spawn()
        .map_err(|error| format!("start {}: {error}", program.display()))?;
    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| "capture native command stdout".to_string())?;
    let stderr = child
        .stderr
        .take()
        .ok_or_else(|| "capture native command stderr".to_string())?;
    let stdout_reader = std::thread::spawn(move || read_command_pipe(stdout));
    let stderr_reader = std::thread::spawn(move || read_command_pipe(stderr));

    let deadline = Instant::now() + timeout;
    let mut timed_out = false;
    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) if Instant::now() < deadline => {
                std::thread::sleep(Duration::from_millis(25));
            }
            Ok(None) => {
                timed_out = true;
                child.kill().map_err(|error| {
                    format!("terminate timed-out {}: {error}", program.display())
                })?;
                break child.wait().map_err(|error| {
                    format!("wait for timed-out {}: {error}", program.display())
                })?;
            }
            Err(error) => {
                return Err(format!("wait for {}: {error}", program.display()));
            }
        }
    };
    let stdout = stdout_reader
        .join()
        .map_err(|_| "native command stdout reader panicked".to_string())??;
    let stderr = stderr_reader
        .join()
        .map_err(|_| "native command stderr reader panicked".to_string())??;
    if timed_out {
        return Err(format!(
            "{} {} timed out after {} seconds",
            program.display(),
            arguments
                .iter()
                .map(|argument| argument.to_string_lossy())
                .collect::<Vec<_>>()
                .join(" "),
            timeout.as_secs()
        ));
    }
    output_text(
        program,
        arguments,
        Output {
            status,
            stdout,
            stderr,
        },
    )
}

fn run_output(program: &Path, arguments: &[OsString]) -> Result<String, String> {
    run_output_with_timeout(program, arguments, Duration::from_secs(10))
}

fn output_text(program: &Path, arguments: &[OsString], output: Output) -> Result<String, String> {
    let stdout = String::from_utf8_lossy(&output.stdout).trim().to_string();
    let stderr = String::from_utf8_lossy(&output.stderr).trim().to_string();
    if !output.status.success() {
        let detail = if stderr.is_empty() { stdout } else { stderr };
        return Err(format!(
            "{} {} failed{}{}",
            program.display(),
            arguments
                .iter()
                .map(|argument| argument.to_string_lossy())
                .collect::<Vec<_>>()
                .join(" "),
            output
                .status
                .code()
                .map(|code| format!(" with exit code {code}"))
                .unwrap_or_default(),
            if detail.is_empty() {
                String::new()
            } else {
                format!(": {detail}")
            },
        ));
    }
    Ok(stdout)
}

fn argument(value: impl AsRef<OsStr>) -> OsString {
    value.as_ref().to_os_string()
}

fn validate_interface_name(name: &str) -> Result<(), String> {
    if cfg!(target_os = "windows") {
        let invalid = ['\\', '/', ':', '*', '?', '"', '<', '>', '|'];
        if name.is_empty()
            || name.chars().count() > 128
            || name
                .chars()
                .any(|character| character <= '\u{1f}' || invalid.contains(&character))
            || name.ends_with([' ', '.'])
        {
            return Err(format!("invalid Windows tunnel name {name:?}"));
        }
        return Ok(());
    }
    if name.is_empty()
        || name.len() > 15
        || !name.bytes().all(|byte| {
            byte.is_ascii_alphanumeric() || matches!(byte, b'_' | b'=' | b'+' | b'.' | b'-')
        })
    {
        return Err(format!("invalid Linux tunnel name {name:?}"));
    }
    Ok(())
}

fn configured_profiles(directory: &Path) -> Result<Vec<(String, PathBuf)>, String> {
    let entries = match fs::read_dir(directory) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => {
            return Err(format!(
                "read configuration directory {}: {error}",
                directory.display()
            ))
        }
    };
    let mut profiles = Vec::new();
    for entry in entries {
        let entry = entry.map_err(|error| format!("read configuration entry: {error}"))?;
        let path = entry.path();
        if !path.is_file()
            || !path
                .extension()
                .and_then(OsStr::to_str)
                .is_some_and(|extension| extension.eq_ignore_ascii_case("conf"))
        {
            continue;
        }
        let Some(name) = path.file_stem().and_then(OsStr::to_str) else {
            continue;
        };
        if validate_interface_name(name).is_ok() {
            profiles.push((name.to_string(), path));
        }
    }
    profiles.sort_by(|left, right| left.0.cmp(&right.0));
    Ok(profiles)
}

fn require_profile(name: &str) -> Result<PathBuf, String> {
    configured_profiles(&config_directory())?
        .into_iter()
        .find_map(|(profile_name, path)| (profile_name == name).then_some(path))
        .ok_or_else(|| format!("tunnel {name} is not configured"))
}

fn backend_versions(state: &BackendState, paths: &NativePaths) -> Result<(String, String), String> {
    let mut cached = state
        .versions
        .lock()
        .map_err(|_| "desktop version cache is poisoned".to_string())?;
    if let Some(versions) = cached.as_ref() {
        return Ok(versions.clone());
    }
    let core = run_output(&paths.core, &[argument("version")])?;
    let quick = run_output(&paths.quick, &[argument("version")])?;
    *cached = Some((core.clone(), quick.clone()));
    Ok((core, quick))
}

fn validate_backend_versions(core: &str, quick: &str) -> Result<(), String> {
    let expected = env!("CARGO_PKG_VERSION");
    let expected_core = format!("wg-quic {expected}");
    let expected_quick = format!("wg-quic-quick {expected}");
    if core != expected_core || quick != expected_quick {
        return Err(format!(
            "desktop/runtime version mismatch: expected {expected_core:?} and {expected_quick:?}, got {core:?} and {quick:?}"
        ));
    }
    Ok(())
}

fn snapshot_inner(app: &AppHandle, state: &BackendState) -> Result<DesktopSnapshot, String> {
    let directory = config_directory();
    let mut backend = BackendInfo {
        platform: platform_name(),
        arch: architecture_name(),
        config_directory: directory.to_string_lossy().into_owned(),
        supported: cfg!(any(target_os = "windows", target_os = "linux")),
        core_version: None,
        quick_version: None,
        error: None,
    };
    let paths = match native_paths(app) {
        Ok(paths) => paths,
        Err(error) => {
            backend.error = Some(error);
            return Ok(DesktopSnapshot {
                backend,
                tunnels: Vec::new(),
                refreshed_at: unix_timestamp_string(),
            });
        }
    };
    match backend_versions(state, &paths) {
        Ok((core, quick)) => {
            if let Err(error) = validate_backend_versions(&core, &quick) {
                backend.error = Some(error);
            }
            backend.core_version = Some(core);
            backend.quick_version = Some(quick);
        }
        Err(error) => backend.error = Some(error),
    }
    let profiles = match configured_profiles(&directory) {
        Ok(profiles) => profiles,
        Err(error) => {
            backend.error = Some(error);
            Vec::new()
        }
    };
    let tunnels = profiles
        .into_iter()
        .map(|(name, config_path)| {
            let status_result = run_output(
                &paths.core,
                &[argument("show"), argument(&name), argument("--json")],
            )
            .and_then(|output| parse_status(&output, &name));
            match status_result {
                Ok(status) => TunnelView {
                    name,
                    config_path: config_path.to_string_lossy().into_owned(),
                    running: true,
                    status: Some(status),
                    status_detail: None,
                },
                Err(error) => TunnelView {
                    name,
                    config_path: config_path.to_string_lossy().into_owned(),
                    running: false,
                    status: None,
                    status_detail: Some(error),
                },
            }
        })
        .collect();
    Ok(DesktopSnapshot {
        backend,
        tunnels,
        refreshed_at: unix_timestamp_string(),
    })
}

fn unix_timestamp_string() -> String {
    let duration = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default();
    duration.as_millis().to_string()
}

fn parse_status(output: &str, expected_interface: &str) -> Result<Value, String> {
    let status = serde_json::from_str::<Value>(output)
        .map_err(|error| format!("wg-quic returned invalid status JSON: {error}"))?;
    let interface = status.get("interface").and_then(Value::as_str);
    let state = status.get("state").and_then(Value::as_str);
    if interface != Some(expected_interface) || state != Some("up") {
        return Err(format!(
            "wg-quic returned status for interface {:?} in state {:?}",
            interface, state
        ));
    }
    Ok(status)
}

fn run_privileged(
    paths: &NativePaths,
    action: &str,
    name: &str,
    source: Option<&Path>,
    overwrite: bool,
) -> Result<String, String> {
    if cfg!(target_os = "windows") {
        let mut arguments = vec![argument("desktop-client"), argument(action), argument(name)];
        if let Some(source) = source {
            arguments.push(argument(source));
            if overwrite {
                arguments.push(argument("--overwrite"));
            }
        }
        return run_output_with_timeout(&paths.quick, &arguments, Duration::from_secs(100));
    }
    let mut arguments = vec![argument(&paths.quick)];
    if action == "import" {
        arguments.extend([
            argument("desktop-import"),
            argument(name),
            argument(source.ok_or_else(|| "desktop import source is required".to_string())?),
        ]);
        if overwrite {
            arguments.push(argument("--overwrite"));
        }
    } else if action == "check" {
        arguments.extend([argument("check"), argument(name)]);
    } else {
        arguments.extend([argument(action), argument(name)]);
    }
    run_output_with_timeout(Path::new("pkexec"), &arguments, Duration::from_secs(100))
}

#[tauri::command(async)]
fn snapshot(app: AppHandle, state: State<'_, BackendState>) -> Result<DesktopSnapshot, String> {
    snapshot_inner(&app, &state)
}

#[tauri::command(async)]
fn manage_tunnel(
    app: AppHandle,
    state: State<'_, BackendState>,
    name: String,
    action: String,
) -> Result<DesktopSnapshot, String> {
    validate_interface_name(&name)?;
    if action != "up" && action != "down" {
        return Err(format!("unsupported tunnel action {action:?}"));
    }
    require_profile(&name)?;
    let paths = native_paths(&app)?;
    run_privileged(&paths, &action, &name, None, false)?;
    snapshot_inner(&app, &state)
}

#[tauri::command(async)]
fn check_tunnel(app: AppHandle, name: String) -> Result<String, String> {
    validate_interface_name(&name)?;
    require_profile(&name)?;
    let paths = native_paths(&app)?;
    run_privileged(&paths, "check", &name, None, false)
}

#[tauri::command(async)]
fn import_config(
    app: AppHandle,
    state: State<'_, BackendState>,
    source_path: String,
    overwrite: bool,
) -> Result<ImportResult, String> {
    let source = PathBuf::from(source_path);
    if !source.is_file()
        || !source
            .extension()
            .and_then(OsStr::to_str)
            .is_some_and(|extension| extension.eq_ignore_ascii_case("conf"))
    {
        return Err("wg-quic configuration files must use the .conf extension".to_string());
    }
    let name = source
        .file_stem()
        .and_then(OsStr::to_str)
        .ok_or_else(|| "configuration file name is not valid UTF-8".to_string())?
        .to_string();
    validate_interface_name(&name)?;
    let paths = native_paths(&app)?;
    run_output(&paths.quick, &[argument("check"), argument(&source)])?;
    run_privileged(&paths, "import", &name, Some(&source), overwrite)?;
    Ok(ImportResult {
        canceled: false,
        imported_name: name,
        snapshot: snapshot_inner(&app, &state)?,
    })
}

#[tauri::command(async)]
fn open_config_directory() -> Result<String, String> {
    let path = config_directory();
    let mut command = if cfg!(target_os = "windows") {
        let mut command = Command::new("explorer.exe");
        command.arg(&path);
        command
    } else {
        let mut command = Command::new("xdg-open");
        command.arg(&path);
        command
    };
    hide_child_window(&mut command);
    command
        .spawn()
        .map(|_| String::new())
        .map_err(|error| format!("open {}: {error}", path.display()))
}

#[tauri::command]
fn desktop_smoke_settings() -> DesktopSmokeSettings {
    if std::env::var_os("WG_QUIC_DESKTOP_INTEGRATION_SMOKE").as_deref() == Some(OsStr::new("1")) {
        return DesktopSmokeSettings {
            mode: "integration",
            source: std::env::var("WG_QUIC_DESKTOP_SMOKE_CONFIG").ok(),
            name: std::env::var("WG_QUIC_DESKTOP_SMOKE_NAME").ok(),
        };
    }
    if std::env::var_os("WG_QUIC_DESKTOP_SMOKE").as_deref() == Some(OsStr::new("1")) {
        return DesktopSmokeSettings {
            mode: "renderer",
            source: None,
            name: None,
        };
    }
    if std::env::var_os("WG_QUIC_DESKTOP_TRAY_SMOKE").as_deref() == Some(OsStr::new("1")) {
        return DesktopSmokeSettings {
            mode: "tray",
            source: None,
            name: None,
        };
    }
    DesktopSmokeSettings {
        mode: "none",
        source: None,
        name: None,
    }
}

fn write_desktop_smoke_result(message: &str) -> Result<(), String> {
    if let Some(result_path) = std::env::var_os("WG_QUIC_DESKTOP_SMOKE_RESULT") {
        fs::write(&result_path, format!("{message}\n")).map_err(|error| {
            format!(
                "write desktop smoke result {}: {error}",
                PathBuf::from(result_path).display()
            )
        })?;
    }
    Ok(())
}

#[tauri::command]
fn report_desktop_smoke(message: String) -> Result<(), String> {
    write_desktop_smoke_result(&message)
}

#[tauri::command]
fn complete_desktop_smoke(app: AppHandle, message: String, failed: bool) {
    if let Err(error) = write_desktop_smoke_result(&message) {
        eprintln!("{error}");
        app.exit(1);
        return;
    }
    if failed {
        eprintln!("{message}");
        app.exit(1);
    } else {
        println!("{message}");
        app.exit(0);
    }
}

fn show_main_window(app: &AppHandle) -> bool {
    if let Some(window) = app.get_webview_window("main") {
        if window.show().is_err() {
            return false;
        }
        let _ = window.unminimize();
        let _ = window.set_focus();
        return true;
    }
    false
}

#[cfg(target_os = "windows")]
fn setup_windows_tray(app: &mut tauri::App) -> tauri::Result<()> {
    if matches!(desktop_smoke_settings().mode, "renderer" | "integration") {
        return Ok(());
    }
    let menu = MenuBuilder::new(app)
        .text("show", "Open wg-quic")
        .separator()
        .text("quit", "Quit")
        .build()?;
    let mut tray = TrayIconBuilder::with_id("main")
        .menu(&menu)
        .tooltip("wg-quic")
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| match event.id().as_ref() {
            "show" => {
                show_main_window(app);
            }
            "quit" => app.exit(0),
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            if matches!(event, TrayIconEvent::DoubleClick { .. }) {
                show_main_window(tray.app_handle());
            }
        });
    if let Some(icon) = app.default_window_icon().cloned() {
        tray = tray.icon(icon);
    }
    tray.build(app)?;
    Ok(())
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(
            |app, _arguments, _cwd| {
                if show_main_window(app) && desktop_smoke_settings().mode == "tray" {
                    let _ = write_desktop_smoke_result(
                        "wg-quic desktop tray single-instance activated",
                    );
                }
            },
        ))
        .plugin(tauri_plugin_dialog::init())
        .manage(BackendState::default())
        .setup(|_app| {
            #[cfg(target_os = "windows")]
            setup_windows_tray(_app)?;
            Ok(())
        })
        .on_window_event(|_window, _event| {
            #[cfg(target_os = "windows")]
            if _window.label() == "main" {
                let smoke_mode = desktop_smoke_settings().mode;
                if !matches!(smoke_mode, "none" | "tray") {
                    return;
                }
                if let WindowEvent::CloseRequested { api, .. } = _event {
                    api.prevent_close();
                    if _window.hide().is_ok() && smoke_mode == "tray" {
                        let _ = write_desktop_smoke_result("wg-quic desktop tray hidden");
                    }
                }
            }
        })
        .invoke_handler(tauri::generate_handler![
            snapshot,
            manage_tunnel,
            check_tunnel,
            import_config,
            open_config_directory,
            desktop_smoke_settings,
            report_desktop_smoke,
            complete_desktop_smoke,
        ])
        .run(tauri::generate_context!())
        .expect("error while running wg-quic desktop");
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validates_tunnel_names() {
        assert!(validate_interface_name("office").is_ok());
        assert!(validate_interface_name("").is_err());
        assert!(validate_interface_name("bad/name").is_err());
    }

    #[test]
    fn lists_only_valid_configuration_files() {
        let root =
            std::env::temp_dir().join(format!("wg-quic-tauri-profile-test-{}", std::process::id()));
        let _ = fs::remove_dir_all(&root);
        fs::create_dir_all(&root).expect("create profile test directory");
        fs::write(root.join("office.conf"), "test").expect("write valid profile");
        fs::write(root.join("ignore.txt"), "test").expect("write ignored profile");
        let profiles = configured_profiles(&root).expect("list profiles");
        assert_eq!(profiles.len(), 1);
        assert_eq!(profiles[0].0, "office");
        fs::remove_dir_all(root).expect("remove profile test directory");
    }

    #[test]
    fn accepts_only_matching_active_status() {
        let status = parse_status(r#"{"interface":"office","state":"up"}"#, "office")
            .expect("parse active status");
        assert_eq!(status["interface"], "office");
        assert!(parse_status(r#"{"interface":"other","state":"up"}"#, "office").is_err());
        assert!(parse_status(r#"{"interface":"office","state":"down"}"#, "office").is_err());
    }

    #[test]
    fn requires_native_versions_to_match_the_desktop() {
        let version = env!("CARGO_PKG_VERSION");
        assert!(validate_backend_versions(
            &format!("wg-quic {version}"),
            &format!("wg-quic-quick {version}"),
        )
        .is_ok());
        assert!(validate_backend_versions("wg-quic old", "wg-quic-quick old").is_err());
    }
}
