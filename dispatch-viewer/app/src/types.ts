// The Run payload, mirrored from dispatch-viewer/internal/frames/frames.go.
// The shell is gated on schemaVersion; anything else is a banner, not a guess.

export interface Node {
  name: string;
  role: string;
  cli: string;
  model?: string;
  effort?: string;
}

export interface RunEvent {
  node: string;
  type: string; // open|continue|restart|reply|accept|plan|assessment|verdict
  at: string; // RFC3339
  dispatch?: string;
  seq?: number;
  file?: string;
  phase?: string;
  outcome?: string;
  text?: string; // log entry text / message body
}

/** Event enriched with its index into the time-sorted events array. */
export interface IndexedEvent extends RunEvent {
  i: number;
}

export interface Span {
  id: string;
  node: string;
  openedAt?: string;
  repliedAt?: string;
  acceptedAt?: string;
  phase?: string;
  continues: number;
  restarts: number;
}

export interface Issue {
  severity: string; // info|warning|severe|error
  code: string;
  message: string;
  node?: string;
  dispatch?: string;
}

export interface Reply {
  dispatch: string;
  phase: string;
  note: string;
  outcome?: string;
  unmet?: string[];
  at: string;
  repos?: unknown;
  raw: string;
}

export interface LogEntry {
  at: string;
  kind: string;
  text: string;
}

export interface CampaignMeta {
  name: string;
  id: string;
  createdAt: string;
  updatedAt: string;
  outcome?: string;
  outcomeAt?: string;
  policy: Record<string, unknown>;
}

export interface Run {
  schemaVersion: number;
  campaign: CampaignMeta;
  nodes: Node[];
  events: RunEvent[];
  spans: Span[];
  log: LogEntry[];
  replies: Record<string, Record<string, Reply>>;
  messages: Record<string, Record<string, string>>;
  readback?: unknown;
  fleetVerdict?: unknown;
  issues: Issue[];
  timelineValid: boolean;
  issueDefs: Record<string, string>;
}
