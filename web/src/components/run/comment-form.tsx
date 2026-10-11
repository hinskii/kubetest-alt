import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import type { RunDetail } from "@/api/client";
import { deleteComment, runQuery, setComment, type RunKey } from "@/api/queries";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { when } from "@/lib/format";

/** Comment shows a finished run's note and, for developers, edits it. */
export function Comment({ k, run, canEdit }: { k: RunKey; run: RunDetail; canEdit: boolean }) {
  const qc = useQueryClient();
  const [text, setText] = useState(run.comment?.text ?? "");
  const save = useMutation({
    mutationFn: async (value: string) => {
      if (value) await setComment(k, value);
      else await deleteComment(k);
      return value;
    },
    onSuccess: (value) => {
      toast.success(value ? "Comment saved." : "Comment removed.");
      return qc.invalidateQueries({ queryKey: runQuery(k).queryKey });
    },
    onError: (e, value) => toast.error(`Could not ${value ? "save" : "remove"} the comment: ${e.message}`),
  });
  return (
    <div className="space-y-3">
      {run.comment && (
        <blockquote className="border-primary border-l-4 pl-3">
          <p>{run.comment.text}</p>
          <footer className="text-muted-foreground text-sm">
            {[run.comment.by, when(run.comment.at)].filter(Boolean).join(" · ")}
          </footer>
        </blockquote>
      )}
      {!run.finished ? (
        <p className="text-muted-foreground">A comment can be added once the run has finished.</p>
      ) : canEdit ? (
        <form
          className="flex flex-wrap items-center gap-2"
          aria-label="Comment"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate(text.trim());
          }}
        >
          <Input
            className="max-w-xl flex-1"
            aria-label="Comment text"
            maxLength={500}
            placeholder="Why did it fail? Known issue?"
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
          <Button type="submit" variant="outline" disabled={save.isPending}>
            Save
          </Button>
          <p className="text-muted-foreground w-full text-sm">
            Leave it empty and save to remove the comment.
          </p>
        </form>
      ) : null}
    </div>
  );
}
