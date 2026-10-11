import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";

import { deleteTest, type TestKey } from "@/api/queries";
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

/** DeleteTest removes the Test after a confirmation; its run history stays. */
export function DeleteTest({ k }: { k: TestKey }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const del = useMutation({
    mutationFn: () => deleteTest(k),
    onSuccess: async () => {
      toast.success(`Test ${k.ns}/${k.name} deleted. Its run history stays.`);
      await qc.invalidateQueries({ queryKey: ["clusters", k.cluster, "tests"] });
      await navigate({ to: "/clusters/$cluster", params: { cluster: k.cluster } });
    },
    onError: (e) => toast.error(`Could not delete the Test: ${e.message}`),
  });
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button variant="destructive">Delete test</Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete test {k.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            The Test leaves the cluster; its run history, logs and artifacts stay. A schedule stops with it.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            className="bg-destructive hover:bg-destructive/90 text-white"
            onClick={() => del.mutate()}
          >
            Delete test {k.name}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
