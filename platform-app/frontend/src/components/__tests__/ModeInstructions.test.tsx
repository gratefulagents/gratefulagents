import { beforeEach, describe, expect, it, vi } from "vitest";

import { ModeInstructions } from "@/components/ModeInstructions";

const mockUseState = vi.fn();
const mockUseId = vi.fn();
const setOpen = vi.fn();
const defaultInstructions = "Review all changes before applying them.";

type ElementWithProps = {
  props: {
    children?: ElementWithProps[] | ElementWithProps | string | boolean | null;
    className?: string;
    id?: string;
    onClick?: () => void;
    [key: string]: unknown;
  };
};

vi.mock("react", async () => {
  const actual = await vi.importActual<typeof import("react")>("react");

  return {
    ...actual,
    useId: () => mockUseId(),
    useState: () => mockUseState(),
  };
});

function renderModeInstructions(instructions = defaultInstructions): ElementWithProps | null {
  return ModeInstructions({ instructions }) as ElementWithProps | null;
}

function getRootChildren(result: ElementWithProps): ElementWithProps[] {
  return result.props.children as ElementWithProps[];
}

function getButton(result: ElementWithProps): ElementWithProps {
  return getRootChildren(result)[0];
}

function getContent(result: ElementWithProps): ElementWithProps | boolean {
  return getRootChildren(result)[1] as ElementWithProps | boolean;
}

function getInstructionsText(result: ElementWithProps): string {
  const content = getContent(result) as ElementWithProps;
  const scrollContainer = content.props.children as ElementWithProps;
  const pre = scrollContainer.props.children as ElementWithProps;
  return pre.props.children as string;
}

describe("ModeInstructions", () => {
  beforeEach(() => {
    mockUseId.mockReturnValue("mode-instructions-content");
    mockUseState.mockReturnValue([false, setOpen]);
    setOpen.mockReset();
  });

  it("does not render when instructions are missing", () => {
    expect(ModeInstructions({ instructions: undefined })).toBeNull();
  });

  it("is collapsed by default", () => {
    const result = renderModeInstructions();

    expect(result).not.toBeNull();

    const button = getButton(result as ElementWithProps);

    expect(button.props["aria-expanded"]).toBe(false);
    expect(button.props["aria-controls"]).toBe("mode-instructions-content");
    expect(getContent(result as ElementWithProps)).toBeFalsy();
  });

  it("toggles expand and collapse when the header button is clicked", () => {
    const collapsed = renderModeInstructions() as ElementWithProps;

    getButton(collapsed).props.onClick?.();

    expect(setOpen).toHaveBeenCalledTimes(1);
    const expandUpdater = setOpen.mock.calls[0][0] as (value: boolean) => boolean;
    expect(expandUpdater(false)).toBe(true);

    mockUseState.mockReturnValue([true, setOpen]);
    const expanded = renderModeInstructions() as ElementWithProps;
    const expandedButton = getButton(expanded);
    const content = getContent(expanded) as ElementWithProps;

    expect(expandedButton.props["aria-expanded"]).toBe(true);
    expect(content.props.id).toBe("mode-instructions-content");
    expect(getInstructionsText(expanded)).toBe(defaultInstructions);

    expandedButton.props.onClick?.();

    expect(setOpen).toHaveBeenCalledTimes(2);
    const collapseUpdater = setOpen.mock.calls[1][0] as (value: boolean) => boolean;
    expect(collapseUpdater(true)).toBe(false);
  });

  it("renders long instructions inside a bounded native scroll container when expanded", () => {
    const instructions = Array.from({ length: 80 }, (_, index) => `Line ${index + 1}`).join("\n");
    mockUseState.mockReturnValue([true, setOpen]);

    const result = renderModeInstructions(instructions) as ElementWithProps;
    const content = getContent(result) as ElementWithProps;
    const scrollContainer = content.props.children as ElementWithProps;

    expect(scrollContainer.props.className).toContain("max-h-56");
    expect(scrollContainer.props.className).toContain("overflow-y-auto");
  });

  it("shows the full short instructions text after expanding", () => {
    const instructions = "Keep responses concise and evidence-based.";
    mockUseState.mockReturnValue([true, setOpen]);

    const result = renderModeInstructions(instructions) as ElementWithProps;

    expect(getInstructionsText(result)).toBe(instructions);
  });
});
