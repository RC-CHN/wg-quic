// SPDX-License-Identifier: GPL-3.0-or-later
// Read-only UI/API checks. Run with an authenticated disposable guest in each locale.
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {mkdirSync} from 'node:fs';
import path from 'node:path';
const require = createRequire(path.join(process.env.WG_QUIC_BROWSER_MODULES || process.cwd(), 'package.json'));
const {chromium} = require('playwright-core');
const base = process.env.OPNSENSE_TEST_URL;
if (!base || !process.env.OPNSENSE_STORAGE_STATE) throw Error('Set OPNSENSE_TEST_URL and OPNSENSE_STORAGE_STATE');
const zh = process.env.WG_QUIC_TEST_LANGUAGE === 'zh';
const output = process.env.WG_QUIC_REVIEW_SCREENSHOTS;
if (output) mkdirSync(output, {recursive: true});
const browser = await chromium.launch({headless:true,executablePath:process.env.BROWSER_PATH,args:['--no-sandbox']});
try {
    const context = await browser.newContext({ignoreHTTPSErrors:true,storageState:process.env.OPNSENSE_STORAGE_STATE});
    const page = await context.newPage();
    const errors = []; page.on('pageerror', e => errors.push(e.message));
    const api = (url) => page.evaluate(url => new Promise((resolve,reject) => $.getJSON('/api/wireguardquic/' + url).done(resolve).fail(reject)), url);
    const shot = async (name) => { if (output) await page.screenshot({path:path.join(output,`${name}-${zh ? 'zh' : 'en'}.png`)}); };
    for (const [width,height] of [[1440,1000],[1024,768]]) {
        await page.setViewportSize({width,height});
        await page.goto(base + '/ui/wireguardquic/general#peers');
        await page.locator('button.command-edit:visible').first().waitFor();
        assert.equal((await page.locator('html').getAttribute('lang')).startsWith('zh'), zh);
        assert.equal((await page.locator('#mainmenu a[href="/ui/wireguardquic/general#peers"]').innerText()).trim(), zh ? '对端' : 'Peers');
        await page.locator('button.command-edit:visible').first().click();
        await page.locator('#peer-transport-settings table').waitFor();
        const modal = page.locator('.modal.in');
        assert.match(await modal.innerText(), zh ? /拥塞控制/ : /Congestion control/);
        assert.match(await modal.innerText(), zh ? /端点地址/ : /Endpoint address/);
        assert.equal(await page.locator('#peer-transport-settings select').count(),0);
        assert.equal(await page.locator('[id="client.fec_policy"] option[value="balanced"]').textContent(), zh ? '均衡' : 'Balanced');
        const source = await api('client/get_client');
        assert.equal(source.client.fec_policy.balanced.value, zh ? '均衡' : 'Balanced');
        assert.ok('selected' in source.client.fec_policy.balanced);
        // Expanding help must not move primary actions beyond the viewport.
        await modal.locator('[id^="show_all_help_"]').click();
        for (const button of await modal.locator('.modal-footer button').all()) {
            const rect = await button.boundingBox();
            assert.ok(rect && rect.y >= 0 && rect.y + rect.height <= height, 'dialog action outside viewport');
        }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
        await shot(`peer-${width}`);
        await modal.locator('button.close').first().click();
        await page.locator('#tab_instances').click();
        await page.locator('#instances button.command-edit').first().click();
        await page.locator('.modal.in [id="server.congestion"] option[value="auto"]').waitFor({state:'attached'});
        assert.equal(await page.locator('[id="server.congestion"] option[value="auto"]').textContent(), zh ? '自动' : 'Automatic');
        await shot(`instance-${width}`);
        await page.locator('.modal.in button.close').first().click();
        await page.locator('#tab_configbuilder').click();
        await page.locator('#builder-transport').filter({hasText:zh ? '拥塞控制' : 'Congestion control'}).waitFor();
        await page.locator('[id="configbuilder.endpoint"]').fill('vpn.example.com:51820');
        await page.locator('[id="configbuilder.address"]').fill('10.66.0.99/32');
        await page.waitForFunction(() => document.getElementById('configbuilder.output').value.includes('[Peer]'));
        const profile = await page.locator('[id="configbuilder.output"]').inputValue();
        assert.match(profile, /# wg-quic: congestion = (auto|cubic|reno|model)/);
        assert.match(profile, /# wg-quic: obfs = (salamander|none)/);
        assert.match(profile, /\[Peer\]/);
        await shot(`generator-${width}`);
        await page.goto(base + '/ui/wireguardquic/status');
        await page.locator('#grid-wireguardquic-status [role="row"], #grid-wireguardquic-status tbody tr').filter({hasText:/quic[0-9]+/}).first().waitFor();
        const expectedRows = Math.min(50, (await api('service/show')).rows.length);
        await page.waitForFunction(expected => {
            const grid = document.getElementById('grid-wireguardquic-status');
            const rows = [...grid.querySelectorAll('.tabulator-row, tbody tr')];
            const holder = grid.querySelector('.tabulator-tableholder') || grid;
            const area = holder.getBoundingClientRect();
            return rows.length === expected && rows.every(row => {
                const rect = row.getBoundingClientRect();
                return rect.top >= area.top - 2 && rect.bottom <= area.bottom + 2;
            });
        }, expectedRows);
        const statusText = await page.locator('#grid-wireguardquic-status').innerText();
        assert.match(statusText, zh ? /QUIC 会话/ : /QUIC Session/);
        assert.doesNotMatch(statusText, /\b(idle|dialing|reconnecting|established)\b/);
        await shot(`status-${width}`);
    }
    // Render the real widget with real OPNsense base classes and synthetic observations.
    await page.goto(base + '/ui/core/dashboard');
    await page.waitForFunction(() => typeof BaseTableWidget !== 'undefined');
    const widgetResult = await page.evaluate(async () => {
        const {default:Widget} = await import('/ui/js/widgets/WireguardQuic.js');
        const widget = new Widget({});
        const region = $('<section id="wg-quic-widget-review" style="position:fixed;inset:100px 260px auto 260px;background:var(--bs-body-bg,white);z-index:9999;padding:20px;"/>').appendTo('body');
        region.append(widget.getMarkup());
        let enabled = '0';
        const row = {type:'peer',if:'quic0',name:'<img src=x onerror=alert(1)>',endpoint:'vpn.example.com:51820','allowed-ips':'10.0.0.0/24','transfer-rx':0,'transfer-tx':1024,session:'reconnecting','peer-status':'stale','public-key':'review'};
        widget.ajaxCall = async url => url.endsWith('/get') ? {general:{enabled}} : {rows:[{...row}]};
        await widget.onWidgetTick();
        const link = region.find('a').attr('href');
        enabled = '1'; await widget.onWidgetTick();
        const text = region.text();
        const injectedImages = region.find('img').length;
        enabled = '0'; await widget.onWidgetTick();
        enabled = '1'; await widget.onWidgetTick();
        const restored = region.text() === text;
        const warnings = region.find('.error-message').length;
        row.name = 'Demo peer'; row['public-key'] = 'replacement'; await widget.onWidgetTick();
        const stalePeers = region.find('a').filter((_, item) => item.textContent.includes('<img')).length;
        return {link,text,injectedImages,restored,warnings,stalePeers};
    });
    assert.equal(widgetResult.link,'/ui/wireguardquic/general');
    assert.match(widgetResult.text, /0 B/);
    assert.match(widgetResult.text, zh ? /重连中/ : /Reconnecting/);
    assert.equal(widgetResult.injectedImages,0);
    assert.equal(widgetResult.restored,true);
    assert.equal(widgetResult.warnings,0);
    assert.equal(widgetResult.stalePeers,0);
    await shot('widget');
    assert.deepEqual(errors,[]);
    console.log(`OPNsense ${zh ? 'Chinese' : 'English'} UI, dialog bounds, config invariants and widget interactions passed`);
} finally { await browser.close(); }
