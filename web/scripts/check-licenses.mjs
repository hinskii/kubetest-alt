// Fails when a production dependency's license isn't allowed.
// Usage: pnpm licenses list --prod --json | node scripts/check-licenses.mjs "MIT ISC ..."
import { readFileSync } from "node:fs";

const allowed = new Set((process.argv[2] ?? "").split(/\s+/).filter(Boolean));
const byLicense = JSON.parse(readFileSync(0, "utf8"));
const bad = Object.entries(byLicense)
  .filter(([license]) => !allowed.has(license))
  .map(([license, pkgs]) => `${license}: ${pkgs.map((p) => p.name).join(", ")}`);
if (bad.length) {
  console.error(`licenses not allowed:\n${bad.join("\n")}`);
  process.exit(1);
}
console.log(`web-licenses: ${Object.keys(byLicense).join(", ")} — all allowed`);
