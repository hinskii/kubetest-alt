import { duration, shortSha, when } from "./format";

describe("format", () => {
  it("when: UTC, seconds, no millis", () => {
    expect(when("2026-10-07T18:30:05.123+02:00")).toBe("2026-10-07 16:30:05 UTC");
    expect(when(undefined)).toBe("");
    expect(when("nonsense")).toBe("");
  });
  it("duration: like the server pages", () => {
    expect(duration(0)).toBe("");
    expect(duration(5600)).toBe("5.6s");
    expect(duration(184_000)).toBe("3m 4s");
    expect(duration(3_720_000)).toBe("1h 2m");
  });
  it("shortSha", () => expect(shortSha("0123456789abcdef")).toBe("0123456"));
});
