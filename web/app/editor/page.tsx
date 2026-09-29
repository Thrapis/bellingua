import { Suspense } from "react";
import { Editor } from "@/components/editor";

export default function EditorPage() {
  // useSearchParams needs a Suspense boundary in a static export.
  return (
    <Suspense fallback={null}>
      <Editor />
    </Suspense>
  );
}
