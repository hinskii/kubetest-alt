import { ChevronsUpDown } from "lucide-react";
import { useState } from "react";

import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

/**
 * NamespaceCombobox picks a namespace from the list or takes a typed one
 * (the list holds only the namespaces the API may list).
 */
export function NamespaceCombobox({
  id,
  value,
  namespaces,
  onChange,
}: {
  id: string;
  value: string;
  namespaces: string[];
  onChange: (ns: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const pick = (ns: string) => {
    onChange(ns);
    setOpen(false);
    setTyped("");
  };
  const other = typed.trim().toLowerCase();
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          id={id}
          variant="outline"
          role="combobox"
          aria-expanded={open}
          className="w-full justify-between font-normal"
        >
          {value || <span className="text-muted-foreground">Choose a namespace</span>}
          <ChevronsUpDown className="opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-(--radix-popover-trigger-width) p-0" align="start">
        <Command>
          <CommandInput placeholder="Find or type a namespace" value={typed} onValueChange={setTyped} />
          <CommandList>
            <CommandEmpty>No namespace listed.</CommandEmpty>
            <CommandGroup>
              {namespaces.map((ns) => (
                <CommandItem key={ns} value={ns} onSelect={() => pick(ns)}>
                  {ns}
                </CommandItem>
              ))}
            </CommandGroup>
            {other && !namespaces.includes(other) && (
              <CommandGroup forceMount>
                <CommandItem forceMount value={`use:${other}`} onSelect={() => pick(other)}>
                  Use “{other}”
                </CommandItem>
              </CommandGroup>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
