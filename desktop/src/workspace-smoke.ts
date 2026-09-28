import type { DesktopSnapshot } from './types';

// Runs in both the packaged WebView and Chromium. These controls must preserve
// the selected tunnel and never turn navigation into a privileged mutation.
export async function runWorkspaceInteractionSmoke(refresh: () => Promise<void>): Promise<void> {
  const original = window.wgQuic.snapshot;
  const base = await original();
  const snapshot: DesktopSnapshot = { ...base, tunnels: ['home', 'office', 'very-long-tunnel-name'].map((name) => ({
    name, configPath: `/fixture/${name}.conf`, running: false, statusState: 'inactive',
  })) };
  const get = (id: string) => document.getElementById(id)!;
  const assert = (condition: boolean, message: string) => { if (!condition) throw new Error(`workspace interaction: ${message}`); };
  window.wgQuic.snapshot = async () => snapshot;
  const search = get('tunnel-search') as HTMLInputElement;
  try {
    await refresh();
    const name = get('detail-name').textContent;
    search.value = 'office';
    search.dispatchEvent(new Event('input'));
    const visible = document.querySelectorAll<HTMLButtonElement>('.tunnel-item:not(.hidden)');
    assert(visible.length === 1 && visible[0]!.dataset.name === 'office', 'search did not filter tunnels');
    assert(get('detail-name').textContent === name, 'search changed the active selection');
    assert(visible[0]!.tabIndex === 0, 'filtered list has no keyboard entry');
    search.value = 'does-not-exist';
    search.dispatchEvent(new Event('input'));
    assert(!get('no-results').classList.contains('hidden'), 'search has no empty state');
    search.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    assert(search.value === '', 'Escape did not clear search');
    const diagnostics = get('tab-diagnostics');
    diagnostics.click();
    await refresh();
    assert(diagnostics.getAttribute('aria-selected') === 'true' && !get('panel-diagnostics').classList.contains('hidden'), 'refresh reset the selected tab');
    diagnostics.focus();
    diagnostics.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }));
    assert(document.activeElement === get('tab-overview') && get('tab-overview').getAttribute('aria-selected') === 'true', 'tabs did not support arrow navigation');
    get('edit-tunnel').focus();
    get('edit-tunnel').dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
    assert(get('detail-name').textContent === name, 'arrow outside the list changed tunnel');
    assert(get('detail-rx').textContent === '—', 'inactive traffic looks like a measured zero');
    const menu = get('tunnel-menu') as HTMLDetailsElement;
    menu.open = true;
    menu.querySelector('summary')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    assert(!menu.open && document.activeElement === menu.querySelector('summary'), 'menu Escape did not restore focus');
  } finally {
    search.value = '';
    window.wgQuic.snapshot = original;
    await refresh();
  }
}
