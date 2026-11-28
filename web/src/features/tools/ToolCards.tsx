import { useEffect, useState } from "react";
import { Check, FileText, HelpCircle, ShieldAlert, Wrench, X } from "lucide-react";
import type { QClient } from "@/client/api";
import type { ArtifactReadResponse, ToolInvocation, UserQuestion } from "@/client/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";

export function ToolInvocationCard({
  invocation,
  onOpenArtifact,
}: {
  invocation: ToolInvocation;
  onOpenArtifact: (artifactId: string) => void;
}) {
  return (
    <Card className="mx-auto w-full max-w-2xl border-border/80 bg-card/70 px-3.5 py-2.5 text-xs">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 font-medium">
          <Wrench className="h-3.5 w-3.5 text-primary" />
          <span className="font-mono">{invocation.tool_name}</span>
          <Badge variant="outline">{invocation.tool_source}</Badge>
        </div>
        <div className="flex items-center gap-2">
          <Badge
            variant={
              invocation.status === "failed" || invocation.status === "rejected"
                ? "destructive"
                : invocation.status === "completed"
                  ? "secondary"
                  : "default"
            }
          >
            {invocation.status}
          </Badge>
          {invocation.artifact_id && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => onOpenArtifact(invocation.artifact_id!)}
            >
              <FileText className="h-3 w-3" />
              Loom
            </Button>
          )}
        </div>
      </div>
      {invocation.result_summary && (
        <div className="mt-1.5 line-clamp-3 whitespace-pre-wrap font-mono text-[11px] text-muted-foreground">
          {invocation.result_summary}
        </div>
      )}
    </Card>
  );
}

export function ApprovalRequestCard({
  invocation,
  onResolve,
}: {
  invocation: ToolInvocation;
  onResolve: (invocationId: string, decision: "approve" | "reject", reason: string) => Promise<void>;
}) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (decision: "approve" | "reject") => {
    setBusy(true);
    try {
      await onResolve(invocation.id, decision, reason);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card className="mx-auto w-full max-w-2xl border-amber-500/50 bg-amber-500/8 p-4 text-xs space-y-2.5">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 font-semibold text-sm">
          <ShieldAlert className="h-4 w-4 text-amber-500" />
          Approval Required: <code className="font-mono">{invocation.tool_name}</code>
        </div>
        <Badge variant="outline">{invocation.gate_mode}</Badge>
      </div>
      {invocation.decision_reason && (
        <div className="text-muted-foreground">{invocation.decision_reason}</div>
      )}
      <pre className="max-h-36 overflow-auto rounded-md border border-border bg-background p-2.5 font-mono text-[11px]">
        {invocation.args_json}
      </pre>
      <div className="flex items-center gap-2">
        <Input
          placeholder="Optional note / rejection reason"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          className="h-8 text-xs"
        />
        <Button size="sm" disabled={busy} onClick={() => void submit("approve")}>
          <Check className="h-3.5 w-3.5" />
          Approve
        </Button>
        <Button size="sm" variant="destructive" disabled={busy} onClick={() => void submit("reject")}>
          <X className="h-3.5 w-3.5" />
          Reject
        </Button>
      </div>
    </Card>
  );
}

export function UserQuestionCard({
  question,
  onAnswer,
}: {
  question: UserQuestion;
  onAnswer: (questionId: string, answerText: string) => Promise<void>;
}) {
  const [freeform, setFreeform] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (text: string) => {
    if (!text.trim()) return;
    setBusy(true);
    try {
      await onAnswer(question.id, text.trim());
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card className="mx-auto w-full max-w-2xl border-primary/40 bg-primary/8 p-4 text-xs space-y-3">
      <div className="flex items-center gap-2 font-semibold text-sm">
        <HelpCircle className="h-4 w-4 text-primary" />
        Question from Character
      </div>
      <div className="text-sm leading-relaxed">{question.prompt}</div>
      {question.choices && question.choices.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {question.choices.map((c) => (
            <Button
              key={c}
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => void submit(c)}
            >
              {c}
            </Button>
          ))}
        </div>
      )}
      {question.allow_freeform && (
        <div className="flex items-center gap-2">
          <Input
            placeholder="Write a custom response…"
            value={freeform}
            onChange={(e) => setFreeform(e.target.value)}
            className="h-8 text-xs"
          />
          <Button size="sm" disabled={busy || !freeform.trim()} onClick={() => void submit(freeform)}>
            Reply
          </Button>
        </div>
      )}
    </Card>
  );
}

export function LoomArtifactDrawer({
  client,
  artifactId,
  onClose,
}: {
  client: QClient;
  artifactId: string | null;
  onClose: () => void;
}) {
  const [data, setData] = useState<ArtifactReadResponse | null>(null);
  const [offset, setOffset] = useState(0);
  const [errMsg, setErrMsg] = useState("");

  useEffect(() => {
    if (!artifactId) {
      setData(null);
      return;
    }
    setErrMsg("");
    void client
      .getArtifact(artifactId, offset, 32768)
      .then(setData)
      .catch((err) => setErrMsg(err instanceof Error ? err.message : "Failed to load artifact"));
  }, [client, artifactId, offset]);

  return (
    <Dialog open={Boolean(artifactId)} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>Loom Artifact {artifactId ? `loom://${artifactId}` : ""}</DialogTitle>
          <DialogDescription>
            {data
              ? `${data.artifact.media_type} · ${data.artifact.byte_size} bytes · offset ${offset}`
              : "Loading immutable artifact…"}
          </DialogDescription>
        </DialogHeader>
        {errMsg && <div className="text-xs text-destructive">{errMsg}</div>}
        {data && (
          <>
            <ScrollArea className="h-[420px] rounded-md border border-border bg-muted/40 p-3">
              <pre className="whitespace-pre-wrap font-mono text-xs">{data.content}</pre>
            </ScrollArea>
            <div className="flex justify-between items-center text-xs">
              <Button
                size="sm"
                variant="outline"
                disabled={offset <= 0}
                onClick={() => setOffset(Math.max(0, offset - 32768))}
              >
                Previous 32 KiB
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={!data.truncated}
                onClick={() => setOffset(offset + 32768)}
              >
                Next 32 KiB
              </Button>
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
