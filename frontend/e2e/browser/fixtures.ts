import {
  test as base,
  expect,
  type Page,
  type WebSocketRoute,
} from "@playwright/test";
import { makeStatsMessage, StatsStream } from "../../src/test/helpers";

export { expect, makeStatsMessage, StatsStream };

// Stands in for go-irl's /ws endpoint. Every connection the page opens is
// recorded so tests can push stats or close it like the server would.
export class StatsServer {
  readonly connections: WebSocketRoute[] = [];

  constructor(private page: Page) {}

  async route(port = 8888) {
    await this.page.routeWebSocket(`ws://localhost:${port}/ws`, (ws) => {
      this.connections.push(ws);
    });
  }

  async connected(count = 1) {
    await expect.poll(() => this.connections.length).toBeGreaterThanOrEqual(count);
    return this.connections[count - 1];
  }

  latest() {
    const ws = this.connections[this.connections.length - 1];
    if (!ws) throw new Error("the page has not connected");
    return ws;
  }

  send(message: unknown) {
    this.latest().send(typeof message === "string" ? message : JSON.stringify(message));
  }
}

// Records window.obsstudio.setCurrentScene calls, as OBS would receive them.
async function installObs(page: Page) {
  await page.addInitScript(() => {
    const scenes: string[] = [];
    (window as unknown as { __scenes: string[] }).__scenes = scenes;
    (window as unknown as { obsstudio: object }).obsstudio = {
      setCurrentScene: (name: string) => scenes.push(name),
    };
  });
}

export function scenes(page: Page) {
  return page.evaluate(() => (window as unknown as { __scenes: string[] }).__scenes);
}

export const test = base.extend<{ server: StatsServer; consoleErrors: string[] }>({
  server: async ({ page }, use) => {
    const server = new StatsServer(page);
    await server.route();
    await installObs(page);
    await use(server);
  },
  consoleErrors: async ({ page }, use) => {
    const errors: string[] = [];
    page.on("pageerror", (err) => errors.push(err.message));
    await use(errors);
  },
});
