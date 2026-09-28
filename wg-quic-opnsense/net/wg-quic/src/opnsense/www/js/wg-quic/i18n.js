/* SPDX-License-Identifier: GPL-3.0-or-later */
// The PHP views and dashboard share this catalog. No user data is translated.
export async function translator(language = document.documentElement.lang, fallback = {}) {
    const normalized = language.toLowerCase().replaceAll('_', '-');
    let catalog = {};
    if (['zh', 'zh-cn', 'zh-hans'].includes(normalized)) {
        const response = await fetch('/ui/js/wg-quic/locales/zh.json');
        if (!response.ok) throw new Error('wg-quic translation catalog unavailable');
        catalog = await response.json();
    }
    return (key) => catalog[key] || fallback[key] || key;
}
