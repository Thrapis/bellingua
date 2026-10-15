import { Suspense } from "react";
import { History } from "@/components/history";

export default function HistoryPage() {
  // useSearchParams needs a Suspense boundary in a static export.
  return (
    <Suspense fallback={null}>
      <History />
    </Suspense>
  );
}
