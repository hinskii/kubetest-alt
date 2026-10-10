import { getTheme, setTheme } from "./theme";

describe("theme", () => {
  it("sets data-theme and remembers the choice under the shared key", () => {
    setTheme("dark");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(getTheme()).toBe("dark");
    expect(localStorage.getItem("theme")).toBe("dark");
    setTheme("light");
    expect(getTheme()).toBe("light");
  });
});
