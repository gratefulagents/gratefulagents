import { Video, X } from "lucide-react";
import { cn } from "@/lib/utils";
import type { ImageAttachment, VideoAttachment } from "@/hooks/useImageAttachments";

interface ImageAttachmentStripProps {
  images: ImageAttachment[];
  videos?: VideoAttachment[];
  onRemove: (id: string) => void;
  className?: string;
}

// ImageAttachmentStrip renders pending composer attachments as small previews,
// each with a remove button.
export function ImageAttachmentStrip({
  images,
  videos = [],
  onRemove,
  className,
}: ImageAttachmentStripProps) {
  const attachments = [
    ...images.map((attachment) => ({ attachment, kind: "image" as const })),
    ...videos.map((attachment) => ({ attachment, kind: "video" as const })),
  ];
  if (attachments.length === 0) return null;

  return (
    <div className={cn("flex flex-wrap gap-2 px-2 pt-2", className)}>
      {attachments.map(({ attachment, kind }) => (
        <div
          key={attachment.id}
          className="group/att relative size-16 overflow-hidden rounded-lg border bg-muted"
          title={attachment.name}
        >
          {kind === "image" ? (
            <img src={attachment.dataUrl} alt={attachment.name} className="size-full object-cover" />
          ) : (
            <>
              <video
                src={attachment.dataUrl}
                muted
                controls={false}
                preload="metadata"
                aria-label={attachment.name}
                className="size-full object-cover"
              />
              <span className="absolute bottom-0.5 left-0.5 inline-flex items-center gap-0.5 rounded bg-background/80 px-1 py-0.5 text-[10px] font-medium text-foreground shadow-sm">
                <Video aria-hidden="true" className="size-3" />
                Video
              </span>
            </>
          )}
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              onRemove(attachment.id);
            }}
            aria-label={`Remove ${attachment.name}`}
            className="absolute right-0.5 top-0.5 inline-flex size-4 items-center justify-center rounded-full bg-background/80 text-foreground shadow-sm hover:bg-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
          >
            <X className="size-3" />
          </button>
        </div>
      ))}
    </div>
  );
}
