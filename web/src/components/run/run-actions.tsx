import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import { abortRun, deleteRun, rerun, runQuery, type RunKey } from "@/api/queries";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

/** RunAgain starts the run's Test again with this run's parameters. */
export function RunAgain({ k, name, testRef }: { k: RunKey; name: string; testRef: string }) {
  const navigate = useNavigate();
  const again = useMutation({
    mutationFn: () => rerun(k),
    onSuccess: (started) => {
      toast.success(`Run started again with the parameters of ${name}.`);
      return navigate({
        to: "/clusters/$cluster/runs/$ns/$id",
        params: { cluster: k.cluster, ns: started.namespace, id: started.name },
      });
    },
    onError: (e) => toast.error(`Could not start the run: ${e.message}`),
  });
  return (
    <Button
      variant="outline"
      disabled={again.isPending}
      title={`Start ${testRef} again with this run's parameters`}
      onClick={() => again.mutate()}
    >
      Run again
    </Button>
  );
}

/** AbortRun asks a live run to stop, with an optional reason. */
export function AbortRun({ k }: { k: RunKey }) {
  const qc = useQueryClient();
  const [message, setMessage] = useState("");
  const abort = useMutation({
    mutationFn: () => abortRun(k, message.trim()),
    onSuccess: () => {
      toast.success("Abort requested.");
      setMessage("");
      return qc.invalidateQueries({ queryKey: runQuery(k).queryKey });
    },
    onError: (e) => toast.error(`Could not abort: ${e.message}`),
  });
  return (
    <form
      className="flex flex-wrap items-center gap-2"
      aria-label="Abort"
      onSubmit={(e) => {
        e.preventDefault();
        abort.mutate();
      }}
    >
      <Input
        className="w-72"
        aria-label="Reason"
        placeholder="Reason (optional)"
        maxLength={512}
        value={message}
        onChange={(e) => setMessage(e.target.value)}
      />
      <Button type="submit" variant="destructive" disabled={abort.isPending}>
        Abort run
      </Button>
    </form>
  );
}

/** DeleteRun removes a finished run with its logs and artifacts, after a confirmation. */
export function DeleteRun({ k, name, testRef }: { k: RunKey; name: string; testRef: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const del = useMutation({
    mutationFn: () => deleteRun(k),
    onSuccess: async () => {
      toast.success(`Run ${name} deleted.`);
      await qc.invalidateQueries({ queryKey: ["clusters", k.cluster, "tests"] });
      await navigate({
        to: "/clusters/$cluster/tests/$ns/$name",
        params: { cluster: k.cluster, ns: k.ns, name: testRef },
      });
    },
    onError: (e) => toast.error(`Could not delete: ${e.message}`),
  });
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button variant="destructive">Delete run</Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete run {name}?</AlertDialogTitle>
          <AlertDialogDescription>
            The run, its log and its artifacts are removed for good, from the cluster and from run history.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            className="bg-destructive hover:bg-destructive/90 text-white"
            onClick={() => del.mutate()}
          >
            Delete run {name}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
