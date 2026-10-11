// Fails when a production dependency's license isn't allowed. Licenses are
// SPDX expressions ("MIT AND ISC", "MIT OR Apache-2.0"); spdx-satisfies
// decides whether the allowed ones cover each.
// Usage: pnpm licenses list --prod --json | node scripts/check-licenses.mjs "MIT ISC ..."
import { readFileSync } from "node:fs";

import satisfies from "spdx-satisfies";

const allowed = (process.argv[2] ?? "").split(/\s+/).filter(Boolean);
const byLicense = JSON.parse(readFileSync(0, "utf8"));
const ok = (license) => {
  try {
    return satisfies(license, allowed);
  } catch {
    return false; // not a valid SPDX expression: a person must look
  }
};
const bad = Object.entries(byLicense)
  .filter(([license]) => !ok(license))
  .map(([license, pkgs]) => `${license}: ${pkgs.map((p) => p.name).join(", ")}`);
if (bad.length) {
  console.error(`licenses not allowed:\n${bad.join("\n")}`);
  process.exit(1);
}
console.log(`web-licenses: ${Object.keys(byLicense).join(", ")} — all allowed`);
