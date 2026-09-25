import { describe, expect, it } from "vitest";
import { nextConnectionQuality } from "./connectionQuality";

describe("nextConnectionQuality", () => {
  it("keeps the current state until the history is full", () => {
    expect(nextConnectionQuality("unknown", [])).toBe("unknown");
    expect(nextConnectionQuality("unknown", [0, 0])).toBe("unknown");
    expect(nextConnectionQuality("poor", [0, 0])).toBe("poor");
  });

  it("resolves unknown to poor only on sustained high loss", () => {
    expect(nextConnectionQuality("unknown", [20, 30, 50])).toBe("poor");
    expect(nextConnectionQuality("unknown", [0, 0, 0])).toBe("good");
    expect(nextConnectionQuality("unknown", [10, 10, 10])).toBe("good");
    expect(nextConnectionQuality("unknown", [50, 50, 0])).toBe("good");
  });

  it("drops from good to poor when every sample is at or above 20%", () => {
    expect(nextConnectionQuality("good", [20, 20, 20])).toBe("poor");
    expect(nextConnectionQuality("good", [19.9, 50, 50])).toBe("good");
  });

  it("recovers from poor only when every sample is below 5%", () => {
    expect(nextConnectionQuality("poor", [0, 4.9, 1])).toBe("good");
    expect(nextConnectionQuality("poor", [0, 5, 0])).toBe("poor");
    expect(nextConnectionQuality("poor", [10, 10, 10])).toBe("poor");
  });
});
