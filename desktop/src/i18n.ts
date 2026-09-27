import { chinese } from './messages';

export type Language = 'en' | 'zh';
function initialLanguage(): Language {
  try { const saved = localStorage.getItem('wg-quic-language'); if (saved === 'en' || saved === 'zh') return saved; } catch { /* Optional preference storage. */ }
  return typeof navigator !== 'undefined' && navigator.language.toLowerCase().startsWith('zh') ? 'zh' : 'en';
}
let language = initialLanguage();
export const currentLanguage = (): Language => language;
export function setLanguage(value: Language): void {
  language = value;
  try { localStorage.setItem('wg-quic-language', value); } catch { /* Session-only preference. */ }
}
export function t(message: string, ...values: unknown[]): string {
  const template = language === 'zh' && Object.hasOwn(chinese, message) ? chinese[message]! : message;
  return template.replace(/\{(\d+)\}/g, (placeholder, index: string) => Number(index) < values.length ? String(values[Number(index)]) : placeholder);
}

// Capture only the initial static text nodes. Input values, configuration
// source, user names and runtime data never enter this translation pass.
let staticText: Array<[Text, string]> | undefined;
let staticAttributes: Array<[Element, string, string]> = [];
export function localizeDocument(): void {
  if (!staticText) {
    staticText = [];
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    while (walker.nextNode()) {
      const node = walker.currentNode as Text;
      if (Object.hasOwn(chinese, node.data.trim())) staticText.push([node, node.data]);
    }
    for (const element of Array.from(document.querySelectorAll('[title], [aria-label], [placeholder]'))) {
      for (const attribute of ['title', 'aria-label', 'placeholder']) {
        const original = element.getAttribute(attribute);
        if (original && Object.hasOwn(chinese, original)) staticAttributes.push([element, attribute, original]);
      }
    }
  }
  for (const [node, original] of staticText) node.data = original.replace(original.trim(), t(original.trim()));
  for (const [element, attribute, original] of staticAttributes) element.setAttribute(attribute, t(original));
  document.documentElement.lang = language === 'zh' ? 'zh-CN' : 'en';
}
