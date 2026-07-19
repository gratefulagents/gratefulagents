import { X } from "lucide-react";
import { cn } from "@/lib/utils";
import type { ImageAttachment } from "@/hooks/useImageAttachments";

interface ImageAttachmentStripProps {
  images: ImageAttachment[];
  onRemove: (id: string) => void;
  className?: string;
}

// ImageAttachmentStrip renders pending composer image attachments as small
// thumbnails, each with a remove button.
export function ImageAttachmentStrip({ images, onRemove, className }: ImageAttachmentStripProps) {
  if (images.length === 0) return null;
  return (
    <div className={cn("flex flex-wrap gap-2 px-2 pt-2", className)}>
      {images.map((img) => (
        <div
          key={img.id}
          className="group/att relative size-16 overflow-hidden rounded-lg border bg-muted"
          title={img.name}
        >
          <img src={img.dataUrl} alt={img.name} className="size-full object-cover" />
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              onRemove(img.id);
            }}
            aria-label={`Remove ${img.name}`}
            className="absolute right-0.5 top-0.5 inline-flex size-4 items-center justify-center rounded-full bg-background/80 text-foreground shadow-sm hover:bg-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
          >
            <X className="size-3" />
          </button>
        </div>
      ))}
    </div>
  );
}
