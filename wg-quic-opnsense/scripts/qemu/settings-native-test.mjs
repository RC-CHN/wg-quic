// Changes transport settings and restarts an instance. Disposable guests only.
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import path from 'node:path';
const require = createRequire(path.join(process.env.WG_QUIC_BROWSER_MODULES || process.cwd(), 'package.json'));
const {chromium} = require('playwright-core');
const base = process.env.OPNSENSE_TEST_URL;
if (process.env.WG_QUIC_DISPOSABLE_VM !== '1' || !base || !process.env.OPNSENSE_STORAGE_STATE) {
 throw new Error('Requires WG_QUIC_DISPOSABLE_VM=1, OPNSENSE_TEST_URL and authenticated OPNSENSE_STORAGE_STATE; mutates a disposable fixture');
}
const browser=await chromium.launch({headless:true,executablePath:process.env.BROWSER_PATH,args:['--no-sandbox']});
try {
 const context=await browser.newContext({ignoreHTTPSErrors:true,storageState:process.env.OPNSENSE_STORAGE_STATE});
 const page=await context.newPage();
 const errors=[]; page.on('pageerror',e=>errors.push(e.message));
 await page.goto(base + '/ui/wireguardquic/general#peers');
 await page.waitForTimeout(2000);
 const api=(url,data={},get=false)=>page.evaluate(({url,data,get})=>new Promise((resolve,reject)=>$.ajax({url:'/api/wireguardquic/'+url,type:get?'GET':'POST',data,dataType:'json'}).done(resolve).fail((_,s,e)=>reject(new Error(s+e)))),{url,data,get});
 const servers=await api('server/search_server');const server=servers.rows[0];
 const peers=await api('client/search_client');const peer=peers.rows[0];
 assert.ok(server.uuid && peer.uuid);
 const membership = async () => (await api('client/search_client')).rows.find(x=>x.uuid===peer.uuid).servers;
 const originalMembership = await membership();
 assert.ok(originalMembership);
 assert.equal((await api('client/set_client/'+peer.uuid,{client:{name:peer.name}})).result,'saved');
 assert.deepEqual(await membership(),originalMembership,'partial updates retain instance membership');
 assert.equal((await api('client/set_client/'+peer.uuid,{client:{pubkey:'invalid!',servers:''}})).result,'failed');
 assert.deepEqual(await membership(),originalMembership,'failed updates retain instance membership');
 for (const id of ['', 'missing', '00000000-0000-0000-0000-000000000000']) {
   assert.equal((await api('client/add_client_builder',{configbuilder:{server:id}})).result,'failed');
 }
 const keys=await api('server/key_pair',{},true);
 const built=await api('client/add_client_builder',{configbuilder:{server:server.uuid,name:'membership-test',pubkey:keys.pubkey,tunneladdress:'10.66.0.99/32'}});
 assert.equal(built.result,'saved',JSON.stringify(built));
 try {
   const created=(await api('client/search_client')).rows.find(x=>x.uuid===built.uuid);
   assert.ok(created.servers.includes(server.uuid),'generator attaches the new peer atomically');
 } finally { await api('client/del_client/'+built.uuid); }
 console.log('Partial updates, failed validation and generated peer membership passed');

 const rejected=await api('client/set_client/'+peer.uuid,{client:{fec:'off'}});
 assert.equal(rejected.result,'failed');
 await page.locator('button.command-edit:visible').first().click();
 await page.locator('#peer-transport-settings table').waitFor();
 assert.ok((await page.locator('#peer-transport-settings').innerText()).includes(server.name));
 assert.equal(await page.locator('#peer-transport-settings select').count(),0);
 const popupPromise=context.waitForEvent('page');
 await page.locator('#peer-transport-settings a').click();
 const popup=await popupPromise;
 await popup.waitForLoadState();
 await popup.locator('.modal.in [id="server.congestion"]').waitFor();
 await popup.close();
 await page.locator('.modal.in button.close').first().click();
 const modes=['auto','cubic','reno'].filter(x=>x!==server.congestion);
 const changed=await api('server/set_server/'+server.uuid,{server:{congestion:modes[0]}});
 assert.equal(changed.result,'saved');
 const applied=await api('service/reconfigure');
 console.log('apply states',applied.instances?.map(x=>x.state));
 const row=applied.instances.find(x=>x.uuid===server.uuid);
 assert.equal(row.state,'restart_required');
 const staleToken=row.restart_token;
 await api('server/set_server/'+server.uuid,{server:{congestion:modes[1]}});
 const rejectedRestart=await api('service/restart_instance/'+server.uuid,{restart_token:staleToken});
 assert.equal(rejectedRestart.instances[0].state,'failed');
 const refreshed=await api('service/reconfigure');
 const fresh=refreshed.instances.find(x=>x.uuid===server.uuid);
 assert.equal(fresh.state,'restart_required');
 const restarted=await api('service/restart_instance/'+server.uuid,{restart_token:fresh.restart_token});
 assert.equal(restarted.status,'ok', JSON.stringify(restarted));
 assert.equal(restarted.instances[0].code,'restarted');
 const again=await api('service/reconfigure');
 assert.equal(again.status,'ok', JSON.stringify(again));
 assert.deepEqual(errors,[]);
 console.log('Native OPNsense readonly transport, deep link, restart gating, stale confirmation and actual restart passed');
}finally{await browser.close();}
