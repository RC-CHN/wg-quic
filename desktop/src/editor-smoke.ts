// Executed by the packaged WebKit/WebView smoke, using the real renderer DOM
// and event handlers. Only writes and confirmation dialogs are substituted;
// the normal refresh and initial key generation still cross the native bridge.
export async function runEditorInteractionSmoke(actions: {
  startNewTunnel(): Promise<void>;
  startEditTunnel(name: string): Promise<void>;
  refresh(): Promise<void>;
  saveForm(): Promise<void>;
  cancelForm(): Promise<void>;
  toggleFormSource(): void;
  selectFormPeer(index: number): void;
  formIsDirty(): boolean;
}): Promise<void> {
  const input = (id: string) => document.getElementById(id) as HTMLInputElement;
  const assert = (condition: boolean, message: string) => {
    if (!condition) throw new Error(`editor interaction: ${message}`);
  };
  const original = { ...window.wgQuic };
  let discard = false;
  window.wgQuic.confirmDiscard = async () => discard;
  try {
    await actions.startNewTunnel();
    const generated = input('form-private-key').value;
    assert(generated.length > 0, 'new tunnel needs a generated key');
    input('form-name').value = 'editor-smoke';
    const address = input('form-addresses');
    address.value = '10.22.0.2/32';
    address.focus();
    address.setSelectionRange(3, 5);
    await new Promise((resolve) => setTimeout(resolve, 2200));
    await actions.refresh();
    assert(address.value === '10.22.0.2/32', 'refresh erased input');
    assert(input('form-private-key').value === generated, 'refresh erased key');
    assert(document.activeElement === address && address.selectionStart === 3, 'refresh moved focus/caret');
    address.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
    assert(address.value === '10.22.0.2/32', 'arrow key changed the draft');
    assert(actions.formIsDirty(), 'edited draft is not marked unsaved');
    await actions.cancelForm();
    assert(!document.getElementById('tunnel-form')!.classList.contains('hidden'), 'cancel ignored keep editing');

    input('form-peer-public-key').value = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=';
    window.wgQuic.writeTunnel = async () => { throw new Error('Synthetic save failure'); };
    await actions.saveForm();
    await actions.refresh();
    assert(!document.getElementById('form-errors')!.classList.contains('hidden'), 'refresh hid the save error');
    assert(address.value === '10.22.0.2/32', 'failed save lost the draft');

    actions.toggleFormSource();
    const source = input('form-source');
    source.value += '\n# preserved comment\n[Peer]\nPublicKey = AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nAllowedIPs = 10.22.0.3/32\n';
    actions.toggleFormSource();
    actions.selectFormPeer(1);
    assert(input('form-allowed-ips').value === '10.22.0.3/32', 'peer selection mixed fields');
    input('form-endpoint').value = 'second.example:443';
    actions.selectFormPeer(0);
    assert(input('form-endpoint').value === '', 'second endpoint leaked into first peer');
    actions.toggleFormSource();
    assert(input('form-source').value.includes('second.example:443'), 'switching peers lost the edit');
    assert(input('form-source').value.includes('# preserved comment'), 'source/form round trip lost comments');
    discard = true;
    await actions.cancelForm();
    assert(input('form-private-key').value === '' && input('form-source').value === '', 'closed editor retained secrets');
  } finally {
    Object.assign(window.wgQuic, original);
  }
}
