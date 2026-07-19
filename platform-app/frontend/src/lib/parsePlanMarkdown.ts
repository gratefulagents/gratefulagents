export interface PlanStep {
  id: string;
  text: string;
  checked: boolean;
  isTask: boolean;
  detail: string;
}

export interface PlanSection {
  id: string;
  title: string;
  steps: PlanStep[];
  content: string;
}

export interface ParsedPlan {
  sections: PlanSection[];
  totalTasks: number;
  completedTasks: number;
  preamble: string;
}

const headerRe = /^#{1,6}\s+(.+)$/;
const taskItemRe = /^[\s]*[-*]\s+\[([ xX])\]\s+(.+)$/;
const unorderedItemRe = /^[\s]*[-*]\s+(?!\[[ xX]\])(.+)$/;
const orderedItemRe = /^[\s]*\d+\.\s+(.+)$/;
const indentedRe = /^( {2}|\t)/;
const codeBlockRe = /^```/;

export function parsePlanMarkdown(md: string): ParsedPlan {
  const lines = md.split("\n");
  const sections: PlanSection[] = [];
  let preamble = "";
  let currentSection: PlanSection | null = null;
  let currentStep: PlanStep | null = null;
  let inCodeBlock = false;
  let contentBuf: string[] = [];
  let sectionIdx = 0;
  let stepIdx = 0;

  function flushContent() {
    const text = contentBuf.join("\n").trim();
    if (text) {
      if (currentSection) {
        currentSection.content += (currentSection.content ? "\n\n" : "") + text;
      } else {
        preamble += (preamble ? "\n\n" : "") + text;
      }
    }
    contentBuf = [];
  }

  function flushStep() {
    if (currentStep && currentSection) {
      currentStep.detail = currentStep.detail.trim();
      currentSection.steps.push(currentStep);
      currentStep = null;
    }
  }

  for (const line of lines) {
    // Track code blocks to avoid parsing their contents
    if (codeBlockRe.test(line)) {
      if (inCodeBlock) {
        inCodeBlock = false;
        if (currentStep) {
          currentStep.detail += line + "\n";
        } else {
          contentBuf.push(line);
        }
        continue;
      }
      inCodeBlock = true;
      if (currentStep) {
        currentStep.detail += line + "\n";
      } else {
        contentBuf.push(line);
      }
      continue;
    }

    if (inCodeBlock) {
      if (currentStep) {
        currentStep.detail += line + "\n";
      } else {
        contentBuf.push(line);
      }
      continue;
    }

    // Header → new section (also resets code block state for robustness)
    const headerMatch = line.match(headerRe);
    if (headerMatch) {
      flushStep();
      flushContent();
      inCodeBlock = false;
      stepIdx = 0;
      currentSection = {
        id: `s-${sectionIdx++}-${headerMatch[1].slice(0, 20)}`,
        title: headerMatch[1],
        steps: [],
        content: "",
      };
      sections.push(currentSection);
      continue;
    }

    // Task list item: - [x] or - [ ]
    const taskMatch = line.match(taskItemRe);
    if (taskMatch) {
      flushStep();
      flushContent();
      if (!currentSection) {
        currentSection = { id: `s-${sectionIdx++}-plan`, title: "Plan", steps: [], content: "" };
        sections.push(currentSection);
      }
      currentStep = {
        id: `${currentSection.id}-step-${stepIdx++}`,
        text: taskMatch[2],
        checked: taskMatch[1].toLowerCase() === "x",
        isTask: true,
        detail: "",
      };
      continue;
    }

    // Unordered list item: - text (negative lookahead excludes task items)
    const ulMatch = line.match(unorderedItemRe);
    if (ulMatch) {
      flushStep();
      flushContent();
      if (!currentSection) {
        currentSection = { id: `s-${sectionIdx++}-plan`, title: "Plan", steps: [], content: "" };
        sections.push(currentSection);
      }
      currentStep = {
        id: `${currentSection.id}-step-${stepIdx++}`,
        text: ulMatch[1],
        checked: false,
        isTask: false,
        detail: "",
      };
      continue;
    }

    // Ordered list item: 1. text
    const olMatch = line.match(orderedItemRe);
    if (olMatch) {
      flushStep();
      flushContent();
      if (!currentSection) {
        currentSection = { id: `s-${sectionIdx++}-plan`, title: "Plan", steps: [], content: "" };
        sections.push(currentSection);
      }
      currentStep = {
        id: `${currentSection.id}-step-${stepIdx++}`,
        text: olMatch[1],
        checked: false,
        isTask: false,
        detail: "",
      };
      continue;
    }

    // Indented line after a step → detail
    if (currentStep && indentedRe.test(line)) {
      currentStep.detail += line + "\n";
      continue;
    }

    // Blank line after a step → flush the step (don't swallow into detail)
    if (currentStep && line.trim() === "") {
      flushStep();
    }

    // Non-indented, non-empty line ends the current step
    if (currentStep && line.trim() !== "") {
      flushStep();
    }

    contentBuf.push(line);
  }

  flushStep();
  flushContent();

  let totalTasks = 0;
  let completedTasks = 0;
  for (const section of sections) {
    for (const step of section.steps) {
      if (!step.isTask) continue;
      totalTasks++;
      if (step.checked) completedTasks++;
    }
  }

  return { sections, totalTasks, completedTasks, preamble };
}
