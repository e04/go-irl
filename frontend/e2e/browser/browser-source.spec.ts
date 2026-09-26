import { expect, makeStatsMessage, scenes, StatsStream, test } from "./fixtures";

const GREEN = "rgb(139, 195, 74)";
const YELLOW = "rgb(255, 193, 7)";
const RED = "rgb(229, 115, 115)";
const GREY = "rgb(207, 216, 220)";

test.beforeEach(async ({ page }) => {
  // Time only moves when a test runs the clock, so the 5s disconnection
  // timeout and the 1s reconnect delay are deterministic.
  await page.clock.install();
});

test.describe("simple", () => {
  test("shows the latest stats and a status dot for the loss rate", async ({
    page,
    server,
    consoleErrors,
  }) => {
    await page.goto("/?type=simple");
    await server.connected();

    const indicator = page.locator("#root > div > div").first();
    await expect(indicator).toHaveCSS("background-color", GREY);

    // The first message only sets the baseline for the retransmission rate.
    const stats = new StatsStream();
    server.send(stats.next({ bitrate: 5.24, rtt: 42.4 }));
    await expect(page.getByText("5.2Mbps")).toBeVisible();
    await expect(page.getByText("42ms")).toBeVisible();
    await expect(page.getByText("-%")).toBeVisible();
    await expect(indicator).toHaveCSS("background-color", GREEN);

    server.send(stats.next({ retransRate: 0 }));
    await expect(page.getByText("0.0%")).toBeVisible();
    await expect(indicator).toHaveCSS("background-color", GREEN);

    server.send(stats.next({ retransRate: 10 }));
    await expect(page.getByText("10.0%")).toBeVisible();
    await expect(indicator).toHaveCSS("background-color", YELLOW);

    server.send(stats.next({ retransRate: 30 }));
    await expect(page.getByText("30.0%")).toBeVisible();
    await expect(indicator).toHaveCSS("background-color", RED);

    expect(consoleErrors).toEqual([]);
  });

  test("ignores malformed and writer messages", async ({ page, server }) => {
    await page.goto("/");
    await server.connected();

    server.send(makeStatsMessage({ bitrate: 3 }));
    await expect(page.getByText("3.0Mbps")).toBeVisible();

    server.send("not json");
    server.send({ type: "reader" });
    server.send(makeStatsMessage({ bitrate: 9, type: "writer" }));
    server.send(makeStatsMessage({ bitrate: 4 }));

    await expect(page.getByText("4.0Mbps")).toBeVisible();
    await expect(page.getByText("9.0Mbps")).toHaveCount(0);
  });

  test("hides the stats after 5s without messages", async ({ page, server }) => {
    await page.goto("/");
    await server.connected();

    server.send(makeStatsMessage({ bitrate: 6 }));
    await expect(page.getByText("6.0Mbps")).toBeVisible();

    await page.clock.runFor(6000);
    await expect(page.getByText("6.0Mbps")).toBeHidden();
    await expect(page.locator("#root > div > div").first()).toHaveCSS(
      "background-color",
      GREY
    );
  });
});

test.describe("scene switching", () => {
  test("switches to online on a healthy link and offline on sustained retransmission", async ({
    page,
    server,
  }) => {
    await page.goto("/");
    await server.connected();

    const stats = new StatsStream();
    for (let i = 0; i < 4; i++) {
      server.send(stats.next({ retransRate: 0 }));
    }
    await expect.poll(() => scenes(page)).toEqual(["ONLINE"]);

    // One lossy sample is not enough to leave the online scene.
    server.send(stats.next({ retransRate: 50 }));
    server.send(stats.next({ retransRate: 0 }));
    for (let i = 0; i < 3; i++) {
      server.send(stats.next({ retransRate: 25 }));
    }
    await expect.poll(() => scenes(page)).toEqual(["ONLINE", "OFFLINE"]);

    for (let i = 0; i < 3; i++) {
      server.send(stats.next({ retransRate: 1 }));
    }
    await expect.poll(() => scenes(page)).toEqual(["ONLINE", "OFFLINE", "ONLINE"]);
  });

  test("uses the scene names from the URL", async ({ page, server }) => {
    await page.goto("/?onlineSceneName=Live&offlineSceneName=BRB");
    await server.connected();

    const stats = new StatsStream();
    for (let i = 0; i < 4; i++) {
      server.send(stats.next());
    }
    await expect.poll(() => scenes(page)).toEqual(["Live"]);

    await page.clock.runFor(6000);
    await expect.poll(() => scenes(page)).toEqual(["Live", "BRB"]);
  });

  test("goes offline when stats stop and back online once they recover", async ({
    page,
    server,
  }) => {
    await page.goto("/");
    await server.connected();

    const stats = new StatsStream();
    for (let i = 0; i < 4; i++) {
      server.send(stats.next());
    }
    await expect.poll(() => scenes(page)).toEqual(["ONLINE"]);

    await page.clock.runFor(6000);
    await expect.poll(() => scenes(page)).toEqual(["ONLINE", "OFFLINE"]);

    // The first message after the gap only sets a new baseline, and one
    // sample after that is not enough to go back online.
    server.send(stats.next());
    server.send(stats.next());
    await expect(page.getByText("0.0%")).toBeVisible();
    expect(await scenes(page)).toEqual(["ONLINE", "OFFLINE"]);

    server.send(stats.next());
    server.send(stats.next());
    await expect.poll(() => scenes(page)).toEqual(["ONLINE", "OFFLINE", "ONLINE"]);
  });
});

test.describe("connection", () => {
  test("reconnects after the server closes the socket", async ({ page, server }) => {
    await page.goto("/");
    const first = await server.connected(1);

    server.send(makeStatsMessage({ bitrate: 1 }));
    await expect(page.getByText("1.0Mbps")).toBeVisible();

    await first.close();
    await page.clock.runFor(1000);
    await server.connected(2);

    server.send(makeStatsMessage({ bitrate: 2 }));
    await expect(page.getByText("2.0Mbps")).toBeVisible();
  });

  test("connects to the port given by wsport", async ({ page, server }) => {
    await server.route(9999);
    await page.goto("/?wsport=9999");
    const ws = await server.connected();
    expect(ws.url()).toBe("ws://localhost:9999/ws");
  });
});

test.describe("other display types", () => {
  test("graph draws a chart and shows the latest stats", async ({
    page,
    server,
    consoleErrors,
  }) => {
    await page.goto("/?type=graph");
    await server.connected();

    const canvas = page.locator("canvas");
    await expect(canvas).toBeVisible();

    const stats = new StatsStream();
    for (let i = 0; i < 4; i++) {
      server.send(
        stats.next({
          bitrate: 7.5,
          rtt: 80,
          retransRate: 2,
          linkBytes: { 1: 600_000, 2: 400_000 },
        })
      );
    }
    await expect(page.getByText("7.5Mbps")).toBeVisible();
    await expect(page.getByText("80ms")).toBeVisible();
    await expect(page.getByText("2.0%")).toBeVisible();
    await expect.poll(() => scenes(page)).toEqual(["ONLINE"]);

    expect(consoleErrors).toEqual([]);
  });

  test("none renders nothing but still switches scenes", async ({ page, server }) => {
    await page.goto("/?type=none");
    await server.connected();

    const stats = new StatsStream();
    for (let i = 0; i < 4; i++) {
      server.send(stats.next());
    }
    await expect.poll(() => scenes(page)).toEqual(["ONLINE"]);
    await expect(page.locator("#root")).toBeEmpty();
  });
});
