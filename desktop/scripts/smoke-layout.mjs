import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { installUIFixture } from "./ui-fixture.mjs";

export async function runLayoutSmoke(call) {
  const evaluate = async (expression) => {
    const response = await call("Runtime.evaluate", {
      expression,
      awaitPromise: true,
      returnByValue: true,
    });
    if (response.exceptionDetails)
      throw Error(
        response.exceptionDetails.exception?.description ||
          "Layout evaluation failed",
      );
    return response.result?.value;
  };
  const click = async (id) => {
    const point = await evaluate(
      `(() => { const e = document.getElementById(${JSON.stringify(id)}); e.scrollIntoView({block:'nearest'}); const r=e.getBoundingClientRect(); const x=r.x+r.width/2,y=r.y+r.height/2; if(!e.contains(document.elementFromPoint(x,y))) throw Error('Obscured control: '+e.id); return {x,y}; })()`,
    );
    await call("Input.dispatchMouseEvent", {
      type: "mousePressed",
      button: "left",
      clickCount: 1,
      ...point,
    });
    await call("Input.dispatchMouseEvent", {
      type: "mouseReleased",
      button: "left",
      clickCount: 1,
      ...point,
    });
    await new Promise((resolve) => setTimeout(resolve, 40));
  };
  await evaluate(`window.wgQuic.confirmDiscard = async () => true`);
  await click("form-cancel");
  await evaluate(`(${installUIFixture.toString()})()`);
  await click("refresh");
  for (const size of [
    { width: 1180, height: 760 },
    { width: 920, height: 620 },
  ]) {
    await call("Emulation.setDeviceMetricsOverride", {
      ...size,
      deviceScaleFactor: 1,
      mobile: false,
    });
    for (const language of ["en", "zh"])
      for (const theme of ["light", "dark"]) {
        await evaluate(
          `(() => { const e=document.getElementById('language-select'); e.value=${JSON.stringify(language)}; e.dispatchEvent(new Event('change')); })()`,
        );
        if (
          (await evaluate("document.documentElement.dataset.theme")) !== theme
        )
          await click("theme-toggle");
        for (const view of ["overview", "diagnostics", "editor", "source"]) {
          await click(
            view === "editor"
              ? "edit-tunnel"
              : view === "source"
                ? "form-source-toggle"
                : `tab-${view}`,
          );
          const problems = await evaluate(`(() => {
          const errors=[];
          for (const selector of ['.workspace','.detail-pane','.detail-scroll','.form-scroll']) {
            const e=document.querySelector(selector); if(!e?.getClientRects().length)continue;
            if(e.scrollWidth>e.clientWidth+2)errors.push(selector+' overflows horizontally');
          }
          for(const id of ['toggle-tunnel','edit-tunnel','form-save','form-cancel']) {
            const e=document.getElementById(id); if(!e?.getClientRects().length)continue;
            const r=e.getBoundingClientRect();
            if(r.x<0||r.right>innerWidth||r.y<0||r.bottom>innerHeight)errors.push(id+' outside window');
            if(!e.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2)))errors.push(id+' obscured');
          }
          return errors;
        })()`);
          if (problems.length)
            throw Error(
              `${size.width}/${language}/${theme}/${view}: ${problems.join(", ")}`,
            );
          if (process.env.WG_QUIC_REVIEW_SCREENSHOTS) {
            mkdirSync(process.env.WG_QUIC_REVIEW_SCREENSHOTS, {
              recursive: true,
            });
            const { data } = await call("Page.captureScreenshot", {
              format: "png",
            });
            writeFileSync(
              path.join(
                process.env.WG_QUIC_REVIEW_SCREENSHOTS,
                `${size.width}-${language}-${theme}-${view}.png`,
              ),
              Buffer.from(data, "base64"),
            );
          }
        }
        await click("form-cancel");
      }
  }
  // A long imported Windows name must wrap without pushing primary controls away.
  await evaluate(
    `window.__uiReview.snapshot.tunnels[0].name='very-long-imported-tunnel-name-with-many-segments';`,
  );
  await click("refresh");
  const overflow = await evaluate(
    `document.querySelector('.detail-pane').scrollWidth > document.querySelector('.detail-pane').clientWidth`,
  );
  if (overflow) throw Error("Long tunnel name overflows the workspace");
  console.log(
    "Desktop layout passed: 32 size/language/theme/view combinations and real pointer clicks",
  );
}
