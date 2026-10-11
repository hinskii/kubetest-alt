import { z } from "zod";

// No run-time code generation: Control Center's CSP allows no eval, and
// Zod's eval probe would otherwise be reported as a CSP violation.
z.config({ jitless: true });
