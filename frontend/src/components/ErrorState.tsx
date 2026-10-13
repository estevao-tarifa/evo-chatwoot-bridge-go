import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { AlertCircle, RotateCcw } from "lucide-react";

/** Shared error state for data-backed screens (loading/empty/error are all handled). */
export function ErrorState({
  title = "Erro ao carregar",
  message,
  onRetry,
}: {
  title?: string;
  message?: string;
  onRetry?: () => void;
}) {
  return (
    <Card className="border-destructive/40 bg-destructive/5">
      <CardContent className="flex flex-col items-center justify-center py-12 text-center">
        <div className="h-12 w-12 rounded-full bg-destructive/10 flex items-center justify-center mb-3">
          <AlertCircle className="h-6 w-6 text-destructive" />
        </div>
        <p className="text-sm font-medium text-destructive">{title}</p>
        {message && (
          <p className="text-xs text-muted-foreground mt-1 max-w-md">{message}</p>
        )}
        {onRetry && (
          <Button variant="outline" size="sm" onClick={onRetry} className="mt-4 gap-2">
            <RotateCcw className="h-3.5 w-3.5" />
            Tentar novamente
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
