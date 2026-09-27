import { t } from './i18n';
export async function copyText(text: string): Promise<void> {
  if (!text) throw new Error(t('No public key is available'));
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const focused = document.activeElement as HTMLElement | null;
    const selection = document.getSelection();
    const range = selection?.rangeCount ? selection.getRangeAt(0).cloneRange() : undefined;
    const field = document.createElement('textarea');
    field.value = text;
    field.style.cssText = 'position:fixed;left:-10000px;top:0';
    document.body.append(field);
    field.select();
    const copied = document.execCommand('copy');
    field.remove();
    focused?.focus();
    if (range && selection) { selection.removeAllRanges(); selection.addRange(range); }
    if (!copied) throw new Error(t('Copy failed. Select and copy the public key manually.'));
  }
}
