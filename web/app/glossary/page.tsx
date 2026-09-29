import { Suspense } from "react";
import { Glossary } from "@/components/glossary";

export default function GlossaryPage() {
  // useSearchParams needs a Suspense boundary in a static export.
  return (
    <Suspense fallback={null}>
      <Glossary />
    </Suspense>
  );
}
