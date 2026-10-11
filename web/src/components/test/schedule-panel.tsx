import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import type { Schedule } from "@/api/client";
import { setSchedule, testQuery, type TestKey } from "@/api/queries";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { when } from "@/lib/format";

/** SchedulePanel shows a Test's recurring schedule and, when allowed, edits it. */
export function SchedulePanel({
  k,
  schedule,
  editable,
}: {
  k: TestKey;
  schedule?: Schedule;
  editable: boolean;
}) {
  const qc = useQueryClient();
  const [expr, setExpr] = useState(schedule?.expr ?? "");
  const save = useMutation({
    mutationFn: (value: string) => setSchedule(k, value),
    onSuccess: (res) => {
      setExpr(res.schedule?.expr ?? "");
      const next = res.schedule?.next?.[0];
      toast.success(
        !res.schedule
          ? "Schedule removed."
          : next
            ? `Schedule saved. Next run: ${when(next)}.`
            : "Schedule saved; it never fires.",
      );
      return qc.invalidateQueries({ queryKey: testQuery(k).queryKey });
    },
    onError: (e) => toast.error(e.message),
  });

  return (
    <div className="space-y-3">
      {schedule ? (
        <div className="space-y-1">
          <p>
            Runs on <code className="font-mono">{schedule.expr}</code>
            {schedule.error && (
              <>
                {" "}
                — <span className="text-danger font-medium">invalid</span>{" "}
                <span className="text-muted-foreground">{schedule.error}</span>
              </>
            )}
          </p>
          {schedule.next.length > 0 && (
            <p className="text-muted-foreground text-sm">Next: {schedule.next.map(when).join(" · ")}</p>
          )}
        </div>
      ) : (
        <p className="text-muted-foreground">No recurring schedule — the Test runs only when started.</p>
      )}
      {editable ? (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate(expr);
          }}
        >
          <div className="grid gap-1.5">
            <Label htmlFor="schedule-expr">
              Cron{" "}
              <span className="text-muted-foreground font-normal">
                5 fields, UTC — or prefix <code className="font-mono">CRON_TZ=Europe/Warsaw</code>
              </span>
            </Label>
            <Input
              id="schedule-expr"
              className="w-72 font-mono"
              value={expr}
              placeholder="0 2 * * 1-5"
              maxLength={200}
              onChange={(e) => setExpr(e.target.value)}
            />
          </div>
          <Button type="submit" variant="outline" disabled={save.isPending}>
            Save schedule
          </Button>
          {schedule && (
            <Button
              type="button"
              variant="destructive"
              disabled={save.isPending}
              onClick={() => save.mutate("")}
            >
              Remove schedule
            </Button>
          )}
        </form>
      ) : null}
    </div>
  );
}
