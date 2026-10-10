import { Moon, Sun } from "lucide-react";

import { Button } from "@/components/ui/button";
import { setTheme, useTheme } from "@/lib/theme";

export function ThemeToggle() {
  const theme = useTheme();
  const next = theme === "dark" ? "light" : "dark";
  return (
    <Button variant="ghost" size="icon" aria-label={`Switch to ${next} theme`} onClick={() => setTheme(next)}>
      {theme === "dark" ? <Sun /> : <Moon />}
    </Button>
  );
}
