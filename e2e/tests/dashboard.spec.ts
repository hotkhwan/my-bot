import { test, expect, Page } from "@playwright/test";

// Each test registers a fresh user (password flow — no Telegram needed) so runs
// are independent against the in-memory user store.
async function registerAndLogin(page: Page) {
  await page.goto("/");
  const user = "e2e_" + Date.now() + "_" + Math.floor(Math.random() * 1e6);
  await page.fill("#username", user);
  await page.fill("#password", "password123");
  // Register creates the account (no session token); Log in then issues the JWT.
  // Await each response so the login never races ahead of the register write.
  await Promise.all([
    page.waitForResponse((r) => r.url().includes("/api/register")),
    page.click("#register"),
  ]);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes("/api/login")),
    page.click("#login"),
  ]);
  await expect(page.locator("#nav")).toBeVisible();
  // After login the default tab is Home (NOVA companion).
  await expect(page.locator("#view-home")).toBeVisible();
}

// Open the Trade tab, where the order ticket, goal form, and command console live.
async function gotoTrade(page: Page) {
  await page.click('#nav button[data-view="orders"]');
  await expect(page.locator("#view-orders")).toBeVisible();
}

test("login → goal paper run shows real stats", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  await page.fill("#g-profit", "5");
  await page.fill("#g-capital", "100");
  await page.fill("#g-risk", "30");
  await page.fill("#g-symbol", "BTC");
  await page.selectOption("#g-strategy", "ema");
  await page.selectOption("#g-interval", "1h");
  await page.click("#g-run");

  const stats = page.locator("#g-stats");
  await expect(stats).toBeVisible({ timeout: 20_000 });
  // Real, deterministic outcome on the stub uptrend: target reached at 100% WR.
  await expect(stats).toContainText("Target reached");
  await expect(stats).toContainText("100%");
  // Equity curve drawn, trades listed, and the run accumulates into history.
  await expect(page.locator("#g-spark svg")).toBeVisible();
  await expect(page.locator("#g-history")).toContainText("BTCUSDT");
});

test("trade tab shows the goal form and pages navigate", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  await expect(page.locator("#g-run")).toBeVisible();
  await expect(page.locator("#g-symbol")).toBeVisible();
  // The old order ticket is gone.
  await expect(page.locator("#side-long")).toHaveCount(0);

  await page.click('#nav button[data-view="history"]');
  await expect(page.locator("#view-history")).toBeVisible();
  await expect(page).toHaveURL(/\/history$/);

  await page.click('#nav button[data-view="settings"]');
  await expect(page.locator("#view-settings")).toBeVisible();
  await expect(page).toHaveURL(/\/settings$/);

  await page.click('#nav button[data-view="orders"]');
  await expect(page.locator("#view-orders")).toBeVisible();
});

test("malformed trade surfaces the parser's specific guidance", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  // Missing the "usdt" suffix on size — the parser explains exactly that.
  await page.fill("#cmd", "long BTC 3x entry 67500 sl 65000 tp 72000 size 100");
  await page.click("#cmd-run");
  await expect(page.locator("#cmd-out")).toContainText("usdt");
  await expect(page.locator("#cmd-out")).not.toContainText("Unknown command");
});

test("flight recorder logs the paper run, labeled and hashed", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);
  await page.fill("#g-profit", "5");
  await page.fill("#g-symbol", "BTC");
  await page.click("#g-run");
  await expect(page.locator("#g-stats")).toBeVisible({ timeout: 20_000 });

  await page.click('#nav button[data-view="history"]');
  await expect(page.locator("#view-history")).toBeVisible();
  // The run shows up in the Flight Recorder, tagged PAPER, with a Merkle root.
  await expect(page.locator("#rec-feed")).toContainText("BTCUSDT");
  await expect(page.locator("#rec-feed .flag.paper").first()).toBeVisible();
  await expect(page.locator("#rec-merkle")).toContainText("Merkle root");
  await expect(page.locator("#rec-stats")).toContainText("Paper runs");
});

test("goal run with AI toggle falls back gracefully (no key configured)", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  await page.check("#g-ai");
  await page.fill("#g-profit", "5");
  await page.fill("#g-symbol", "BTC");
  await page.click("#g-run");

  // AI is not configured in the harness → it must still produce a real run and
  // tell the user it used the rule-based strategy.
  await expect(page.locator("#g-stats")).toBeVisible({ timeout: 20_000 });
  await expect(page.locator("#g-msg")).toContainText("rule-based");
});
