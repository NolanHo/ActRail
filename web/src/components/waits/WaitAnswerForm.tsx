import { useState } from "preact/hooks";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";

interface WaitAnswerFormProps {
  disabled?: boolean;
  submitting?: boolean;
  onSubmit(answer: string): void;
}

export function WaitAnswerForm({ disabled = false, submitting = false, onSubmit }: WaitAnswerFormProps) {
  const [answer, setAnswer] = useState("");
  const trimmed = answer.trim();
  return (
    <form
      className="waitAnswerForm"
      onSubmit={(event) => {
        event.preventDefault();
        if (!trimmed || disabled || submitting) {
          return;
        }
        onSubmit(trimmed);
      }}
    >
      <label className="waitAnswerLabel" htmlFor="wait-answer-input">Answer</label>
      <Textarea
        id="wait-answer-input"
        value={answer}
        className="waitAnswerTextarea"
        disabled={disabled || submitting}
        placeholder="Type the answer for the blocked agent"
        onInput={(event) => setAnswer(event.currentTarget.value)}
      />
      <Button type="submit" disabled={disabled || submitting || !trimmed}>{submitting ? "Submitting..." : "Submit answer"}</Button>
    </form>
  );
}
