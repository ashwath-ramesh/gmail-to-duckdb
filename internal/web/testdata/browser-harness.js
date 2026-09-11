const url = process.argv[2];
const chrome = process.argv[3];
const pwPath = process.argv[4];
const { chromium } = require(pwPath);

const ALPHA = "alphatoken";
const BETA = "betatoken";
const ALPHA_PAGE = 50;
const ALPHA_TOTAL = Number(process.argv[5] || 110);
const BETA_TOTAL = Number(process.argv[6] || 20);
const BETA_UNREAD = Number(process.argv[7] || 10);

function median(xs) {
  const a = xs.slice().sort((x, y) => x - y);
  const n = a.length;
  if (n === 0) return 0;
  if (n % 2 === 1) return a[(n - 1) / 2];
  return (a[n / 2 - 1] + a[n / 2]) / 2;
}

function p95(xs) {
  const a = xs.slice().sort((x, y) => x - y);
  const n = a.length;
  if (n === 0) return 0;
  return a[Math.max(0, Math.min(n - 1, Math.ceil(0.95 * n) - 1))];
}

function requestQ(r) {
  return new URL(r.url()).searchParams.get("q") || "";
}


async function twoRAF(page) {
  await page.evaluate(() => new Promise((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(resolve));
  }));
}

async function installTrackingFetch(page) {
  await page.evaluate(() => {
    window.__reqSeq = 0;
    window.__reqs = [];
    window.__lastFetchDone = { gen: 0, q: "", t: 0 };
    window.__fetchURL = (input) => {
      if (typeof input === "string") return new URL(input, location.origin);
      if (input instanceof URL) return input;
      return new URL(input.url, location.origin);
    };
    window.__ignoreAbort = false;
    window.__origFetch = window.fetch.bind(window);
    window.fetch = (input, init) => {
      const next = window.__ignoreAbort ? { ...(init || {}) } : init;
      if (window.__ignoreAbort) delete next.signal;
      const u = window.__fetchURL(input);
      const q = u.searchParams.get("q") || "";
      const isMsgs = u.pathname === "/api/messages";
      let gen = 0;
      if (isMsgs) {
        gen = ++window.__reqSeq;
        window.__reqs.push({
          url: u.href,
          q,
          gen,
          t: performance.now(),
          hasSignal: !!(next && next.signal),
        });
      }
      return window.__origFetch(input, next).then((res) => {
        if (isMsgs) {
          res.clone().text().then(() => {
            window.__lastFetchDone = { gen, q, t: performance.now() };
            if (typeof window.__onFetchBody === "function") window.__onFetchBody();
          }).catch(() => {});
        }
        return res;
      });
    };
  });
}

async function setIgnoreAbort(page, on) {
  await page.evaluate((on) => {
    window.__ignoreAbort = on;
  }, on);
}

async function setQuery(page, q) {
  await page.evaluate((q) => {
    const el = document.querySelector('input[name="q"]');
    window.__inputT0 = null;
    el.addEventListener("input", () => {
      if (el.value === q) window.__inputT0 = performance.now();
    }, { capture: true, once: true });
    el.focus();
    el.value = q;
    el.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertReplacementText", data: q }));
  }, q);
}

async function reqSeq(page) {
  return page.evaluate(() => window.__reqSeq || 0);
}

async function waitCurrent(page, q, n, minGen) {
  await page.waitForFunction(({ q, n, minGen }) => {
    const done = window.__lastFetchDone;
    if (!done || done.gen < minGen || done.q !== q) return false;
    const rows = [...document.querySelectorAll("#list .row")];
    return rows.length === n && rows.every((el) => (el.textContent || "").includes(q));
  }, { q, n, minGen });
}

async function measureQuery(page, q, n) {
  return page.evaluate(({ q, n }) => new Promise((resolve, reject) => {
    const el = document.querySelector('input[name="q"]');
    const list = document.getElementById("list");
    const minGen = window.__reqSeq;
    let stopped = false;
    const committed = () => {
      const done = window.__lastFetchDone;
      if (!done || done.gen <= minGen || done.q !== q) return false;
      const rows = [...list.querySelectorAll(".row")];
      return rows.length === n && rows.every((r) => (r.textContent || "").includes(q));
    };
    const finish = () => {
      if (stopped || !committed()) return;
      stopped = true;
      clearTimeout(timer);
      obs.disconnect();
      window.__onFetchBody = null;
      requestAnimationFrame(() => requestAnimationFrame(() => {
        resolve({
          ms: performance.now() - window.__inputT0,
          rows: list.querySelectorAll(".row").length,
          q,
        });
      }));
    };
    const timer = setTimeout(() => {
      if (stopped) return;
      stopped = true;
      obs.disconnect();
      window.__onFetchBody = null;
      reject(new Error("measure timeout gen=" + minGen + " reqs=" + window.__reqSeq + " rows=" + list.querySelectorAll(".row").length + " doneGen=" + (window.__lastFetchDone && window.__lastFetchDone.gen)));
    }, 30000);
    const obs = new MutationObserver(finish);
    obs.observe(list, { childList: true, subtree: true });
    window.__onFetchBody = finish;
    window.__inputT0 = null;
    el.addEventListener("input", () => {
      if (el.value === q) window.__inputT0 = performance.now();
    }, { capture: true, once: true });
    el.focus();
    el.value = q;
    el.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertReplacementText", data: q }));
  }), { q, n });
}

async function reqCount(page) {
  return page.evaluate(() => window.__reqs.length);
}

(async () => {
  const browser = await chromium.launch({
    executablePath: chrome,
    headless: true,
    args: ["--no-sandbox"],
  });
  const page = await browser.newPage();
  let releaseOld;
  const oldGate = new Promise((resolve) => { releaseOld = resolve; });
  let oldStarted;
  const oldStartedP = new Promise((resolve) => { oldStarted = resolve; });
  let releaseErr;
  const errGate = new Promise((resolve) => { releaseErr = resolve; });
  let errStarted;
  const errStartedP = new Promise((resolve) => { errStarted = resolve; });
  let holdBeta = false;
  let holdErr = false;
  await page.route("**/api/messages**", async (route) => {
    const q = new URL(route.request().url()).searchParams.get("q") || "";
    if (holdBeta && q === BETA) {
      oldStarted();
      await oldGate;
    } else if (holdErr && q === "after:nope") {
      errStarted();
      await errGate;
    }
    await route.continue();
  });
  await page.goto(url, { waitUntil: "domcontentloaded" });
  await page.waitForSelector('input[name="q"]');
  await installTrackingFetch(page);

  const timings = [];
  let rowsAlpha = 0;
  let rowsBeta = 0;
  for (let i = 0; i < 10; i++) {
    const alpha = i % 2 === 0;
    const q = alpha ? ALPHA : BETA;
    const n = alpha ? ALPHA_PAGE : BETA_TOTAL;
    const got = await measureQuery(page, q, n);
    timings.push(got.ms);
    if (alpha) rowsAlpha = got.rows;
    else rowsBeta = got.rows;
  }
  const counts = {
    rows_alpha: rowsAlpha,
    rows_beta: rowsBeta,
  };

  await setIgnoreAbort(page, true);
  holdBeta = true;
  let minGen = (await reqSeq(page)) + 1;
  await setQuery(page, BETA);
  await oldStartedP;
  minGen = (await reqSeq(page)) + 1;
  await setQuery(page, ALPHA);
  await waitCurrent(page, ALPHA, ALPHA_PAGE, minGen);
  const afterNew = ALPHA_PAGE;
  const staleDone = page.waitForResponse(async (r) => {
    if (requestQ(r) !== BETA) return false;
    await r.text();
    return true;
  });
  releaseOld();
  await staleDone;
  await twoRAF(page);
  const afterStale = await page.evaluate((q) => {
    const rows = [...document.querySelectorAll("#list .row")];
    return rows.length === 50 && rows.every((el) => (el.textContent || "").includes(q)) ? 50 : rows.length;
  }, ALPHA);
  counts.rows_after_stale = afterStale;
  if (afterStale !== afterNew) throw new Error("stale success replaced newer results");

  holdErr = true;
  await setQuery(page, "after:nope");
  await errStartedP;
  minGen = (await reqSeq(page)) + 1;
  await setQuery(page, ALPHA);
  await waitCurrent(page, ALPHA, ALPHA_PAGE, minGen);
  const afterErrNew = ALPHA_PAGE;
  const errDone = page.waitForResponse(async (r) => {
    if (requestQ(r) !== "after:nope") return false;
    await r.text();
    return true;
  });
  releaseErr();
  await errDone;
  await twoRAF(page);
  const afterErr = await page.evaluate((q) => {
    const rows = [...document.querySelectorAll("#list .row")];
    return rows.length === 50 && rows.every((el) => (el.textContent || "").includes(q)) ? 50 : rows.length;
  }, ALPHA);
  counts.rows_after_error = afterErr;
  if (afterErr !== afterErrNew) throw new Error("stale error replaced newer results");
  await setIgnoreAbort(page, false);

  const beforeEnter = await reqCount(page);
  await page.evaluate(() => { window.__enterT0 = performance.now(); });
  await page.keyboard.press("Enter");
  await page.waitForFunction((n) => window.__reqs.length > n, beforeEnter);
  const enterDelay = await page.evaluate((n) => window.__reqs[n].t - window.__enterT0, beforeEnter);
  await waitCurrent(page, ALPHA, ALPHA_PAGE, beforeEnter + 1);
  await page.evaluate(() => new Promise((r) => setTimeout(r, 150)));
  const afterEnter = await reqCount(page);
  counts.rows_enter = ALPHA_PAGE;
  counts.enter_requests = afterEnter - beforeEnter;
  if (counts.enter_requests !== 1) throw new Error("enter request count");
  if (enterDelay >= 100) throw new Error("enter used debounce");

  await page.evaluate(() => { document.querySelector('input[name="unread"]').checked = false; });
  const beforeUnread = await reqCount(page);
  await setQuery(page, BETA);
  await page.click('input[name="unread"]');
  await page.waitForFunction((n) => window.__reqs.length > n, beforeUnread);
  await page.evaluate(() => new Promise((r) => setTimeout(r, 150)));
  const unreadReqs = await page.evaluate((n) => window.__reqs.slice(n), beforeUnread);
  if (unreadReqs.length !== 1) throw new Error("unread leftover timer");
  if (!unreadReqs[0].url.includes("unread=1")) throw new Error("unread request missing");
  await waitCurrent(page, BETA, BETA_UNREAD, beforeUnread + 1);
  counts.rows_unread = BETA_UNREAD;

  await page.evaluate(() => { document.querySelector('input[name="unread"]').checked = false; });
  minGen = (await reqSeq(page)) + 1;
  await setQuery(page, ALPHA);
  await waitCurrent(page, ALPHA, ALPHA_PAGE, minGen);
  const more = page.locator("#more");
  if (!(await more.isVisible())) throw new Error("more hidden");
  minGen = (await reqSeq(page)) + 1;
  await more.click();
  await waitCurrent(page, ALPHA, 100, minGen);
  if (!(await more.isVisible())) throw new Error("more hidden at 100");
  minGen = (await reqSeq(page)) + 1;
  await more.click();
  await waitCurrent(page, ALPHA, ALPHA_TOTAL, minGen);
  if (await more.isVisible()) throw new Error("more visible at end");
  counts.rows_final = ALPHA_TOTAL;

  const p50 = median(timings);
  if (p50 < 100) throw new Error("search_p50_below_debounce");
  if (rowsAlpha !== ALPHA_PAGE || rowsBeta !== BETA_TOTAL) {
    throw new Error("disjoint render counts");
  }

  await browser.close();
  process.stdout.write(JSON.stringify({
    counts,
    search_p50_ms: p50,
    search_p95_ms: p95(timings),
    search_runs: timings.length,
    enter_delay_ms: Math.round(enterDelay * 1000) / 1000,
  }));
})().catch((err) => {
  process.stderr.write(String(err && err.message ? err.message : err));
  process.exit(1);
});
