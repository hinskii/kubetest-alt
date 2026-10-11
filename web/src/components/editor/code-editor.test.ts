import { languageOf, linesOf } from "@/components/editor/code-editor";

describe("the code editor", () => {
  it("picks the language from the file name", () => {
    for (const f of ["load.js", "a.MJS", "x.ts", "locustfile.py", "plan.jmx", "s.xml", "t.yaml", "t.yml"]) {
      expect(languageOf(f), f).toHaveLength(1);
    }
    expect(languageOf("README")).toEqual([]);
    expect(languageOf("")).toEqual([]);
  });

  it("finds the lines the server's errors name", () => {
    expect(
      linesOf([
        "YAML: error converting YAML to JSON: yaml: line 3: did not find expected key",
        "Name: required",
        'error unmarshaling JSON: line 12: unknown field "uses"',
      ]),
    ).toEqual([
      { line: 3, message: "YAML: error converting YAML to JSON: yaml: line 3: did not find expected key" },
      { line: 12, message: 'error unmarshaling JSON: line 12: unknown field "uses"' },
    ]);
  });
});
