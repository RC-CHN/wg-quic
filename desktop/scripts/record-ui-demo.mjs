#!/usr/bin/env node
// Optional recording tool. See docs/desktop/REVIEW.md for external dependencies.
import { createServer } from "node:http";
import { readFileSync, mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { installUIFixture } from "./ui-fixture.mjs";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright-core"
);
const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../dist",
);
const output = path.resolve(
  process.env.WG_QUIC_DEMO_DIR || "dist/desktop-redesign",
);
mkdirSync(output, { recursive: true });
const server = createServer((request, response) => {
  const file = path.resolve(
    root,
    `.${new URL(request.url, "http://localhost").pathname === "/" ? "/index.html" : new URL(request.url, "http://localhost").pathname}`,
  );
  if (!file.startsWith(root + path.sep)) {
    response.writeHead(403).end();
    return;
  }
  try {
    response.setHeader(
      "Content-Type",
      file.endsWith(".js")
        ? "text/javascript"
        : file.endsWith(".css")
          ? "text/css"
          : "text/html",
    );
    response.end(readFileSync(file));
  } catch {
    response.writeHead(404).end();
  }
});
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
let browser;
try {
  browser = await chromium.launch({
    executablePath: process.env.BROWSER_PATH,
    headless: true,
    args: ["--no-sandbox"],
  });
  const context = await browser.newContext({
    viewport: { width: 1180, height: 760 },
    recordVideo: { dir: output, size: { width: 1180, height: 760 } },
  });
  await context.addInitScript(installUIFixture);
  await context.addInitScript(() => {
    localStorage.setItem("wg-quic-language", "zh");
    localStorage.setItem("wg-quic-theme", "light");
  });
  const page = await context.newPage();
  const started = Date.now();
  const chapters = [];
  const pause = (ms = 1800) => page.waitForTimeout(ms);
  const caption = (text) => {
    chapters.push({ seconds: (Date.now() - started) / 1000, text });
    console.log(text);
  };
  const click = async (selector) => {
    const target = page.locator(selector);
    await target.scrollIntoViewIfNeeded();
    const box = await target.boundingBox();
    if (!box) throw Error(`Not visible: ${selector}`);
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, {
      steps: 14,
    });
    await pause(250);
    await target.click();
  };
  const type = async (selector, value) => {
    await click(selector);
    await page.locator(selector).fill("");
    await page.locator(selector).pressSequentially(value, { delay: 35 });
  };
  await page.goto(`http://127.0.0.1:${server.address().port}`);
  await page.waitForSelector('body[data-ready="true"]');
  // Visible pointer belongs only to the recording, never to the product.
  await page.evaluate(() => {
    const cursor = document.createElement("div");
    cursor.style.cssText =
      "position:fixed;width:18px;height:18px;border:2px solid #2864dc;background:#699aff44;border-radius:50%;pointer-events:none;z-index:9999;transform:translate(-50%,-50%)";
    document.body.append(cursor);
    document.addEventListener("mousemove", (event) => {
      cursor.style.left = `${event.clientX}px`;
      cursor.style.top = `${event.clientY}px`;
    });
  });
  caption("概览：先看连接状态，再看流量和对端。所有网络状态均为演示数据。");
  await pause(3500);
  await page.screenshot({ path: path.join(output, "overview-light.png") });
  caption("对端卡片：分别查看接收、发送与累计流量；公钥旁即可复制，握手时间按当前语言显示。");
  await page.locator('.peer-handshake').scrollIntoViewIfNeeded();
  await pause(2600);
  await page.screenshot({ path: path.join(output, "peer-card.png") });
  caption("导入配置后自动选中新隧道，是否连接由你决定。");
  await click("#import-config");
  await pause(2200);
  caption("搜索只筛选列表，不改变正在使用的连接。");
  await type("#tunnel-search", "office");
  await pause();
  await click('.tunnel-item[data-name="office"]');
  await click("#tunnel-search");
  await page.keyboard.press("Escape");
  await pause();
  caption("点击连接，进度与认证状态分开显示；不会把正在连接说成已经成功。");
  await click("#toggle-tunnel");
  await pause(3500);
  caption("诊断独立放在次级页面，需要时再查看链路详情和断线原因。");
  await click("#tab-diagnostics");
  await pause(2200);
  await click("#session-history > summary");
  await pause(2200);
  caption("新建按本机、对端、可选设置分组；底部保存操作始终可见。");
  await click("#new-tunnel");
  await pause(1800);
  caption("漏填时，错误落到具体字段，自动定位；不让用户猜哪里出了问题。");
  await click("#form-save");
  await pause(2300);
  await type("#form-name", "travel");
  await type("#form-addresses", "10.24.0.3/32");
  await type(
    "#form-peer-public-key",
    "AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
  );
  await type("#form-endpoint", "vpn.example.com:51820");
  await type("#form-allowed-ips", "10.24.0.0/16");
  await pause(1800);
  caption("公钥可以直接分享。高级设置明确作用于整条隧道，常用信息保持简洁。");
  await click("#form-advanced > summary");
  await page.locator("#form-fec").scrollIntoViewIfNeeded();
  await page.selectOption("#form-fec", "off");
  await pause(2200);
  await page.selectOption("#form-fec", "auto");
  caption("配置原文与表单可以往返切换，保留注释、扩展字段和其他对端。");
  await click("#form-source-toggle");
  await pause(2500);
  await click("#form-source-toggle");
  caption("模拟一次保存失败：内容继续保留，可以修正后重试。");
  await page.evaluate(() => {
    window.__uiReview.failSave = true;
  });
  await click("#form-save");
  await pause(2500);
  caption("保存只写入配置，不会悄悄连接，也不会打断已有连接。");
  await click("#form-save");
  await pause(2500);
  await click("#toggle-tunnel");
  await pause(3000);
  caption("修改运行中的隧道：先保存，再明确应用，始终知道哪一步已经完成。");
  await click("#edit-tunnel");
  await type("#form-allowed-ips", "10.24.0.0/16, 192.168.10.0/24");
  await page.keyboard.press("Control+s");
  await pause(2300);
  await click("#apply-config");
  await pause(2200);
  caption("中英文、明暗主题使用同一套布局；切换时保持当前上下文。");
  await click("#theme-toggle");
  await pause(1800);
  await page.selectOption("#language-select", "en");
  await pause(2200);
  await page.screenshot({ path: path.join(output, "overview-dark-en.png") });
  await page.selectOption("#language-select", "zh");
  await pause(2000);
  caption(
    "新版桌面交互演示结束。界面为真实构建，连接与保存使用隔离的模拟后端。",
  );
  await pause(3500);
  const duration = (Date.now() - started) / 1000;
  const video = page.video();
  await context.close();
  await video.saveAs(path.join(output, "demo-raw.webm"));
  const timestamp = (seconds) => {
    const ms = Math.round(seconds * 1000);
    return `${String(Math.floor(ms / 3600000)).padStart(2, "0")}:${String(Math.floor(ms / 60000) % 60).padStart(2, "0")}:${String(Math.floor(ms / 1000) % 60).padStart(2, "0")},${String(ms % 1000).padStart(3, "0")}`;
  };
  writeFileSync(
    path.join(output, "demo.srt"),
    chapters
      .map(
        (chapter, i) =>
          `${i + 1}\n${timestamp(chapter.seconds)} --> ${timestamp(chapters[i + 1]?.seconds || duration)}\n${chapter.text}\n`,
      )
      .join("\n"),
  );
  writeFileSync(
    path.join(output, "chapters.json"),
    JSON.stringify({ duration, chapters }, null, 2),
  );
  console.log(`Recorded ${duration.toFixed(1)} seconds to ${output}`);
} finally {
  await browser?.close();
  server.close();
}
