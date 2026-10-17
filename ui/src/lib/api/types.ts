// Hand-typed mirror of internal/session.Session and adjacent shapes.
// We deliberately don't generate these from Go, because the surface is small
// enough that the cost of a generator (tygo etc) outweighs the drift
// risk. Keep this file 1:1 with internal/session/session.go JSON tags.

export type SessionStatus = 'created' | 'running' | 'starting' | 'stopped' | 'errored';

export interface Session {
  id: string;
  name: string;
  agent: string;
  // Which sandbox runtime hosts this session ("applevf", "cloudhv", "sandboxless").
  runtime: string;
  image: string;
  owner_email: string;
  status: SessionStatus;
  // Why deepcove stopped this session on its own. Absent when you stopped it.
  stop_reason?: 'unresponsive' | 'runtime_missing' | 'sandbox_lost';
  env: Record<string, string> | null;
  // User mounts currently attached to the sandbox. The agent's /root
  // home is NOT included, because deepcove manages it, not the user.
  mounts: MountRequest[] | null;
  // Empty means "use the agent registry's default".
  setup_script: string;
  sandbox_id?: string;
  // On this node's shared machine, rather than one of its own.
  on_workspace_vm?: boolean;
  // How this session's agent logs in. On the workspace VM, shared means the machine's login.
  login_mode: LoginMode;
  // The folder this session works in, inside the sandbox.
  work_dir?: string;
  // Where the agent actually works, answered by the runtime. The one place the path comes from when
  // deepcove made the folder rather than you picking it.
  agent_work_dir?: string;
  created_at: string; // ISO-8601 timestamp
  updated_at: string;
  last_activity_at: string; // added in Phase 4 / migration 0002
  // Per session CPU/memory/disk caps. A zero field means "use the runtime's default".
  resources: SessionResources;
  // Set only on the aggregated list (GET /api/sessions): the node hosting this session.
  host?: SessionHost;
}

// SessionResources mirrors internal/session.Resources. Every field is optional and zero means the
// runtime picks. Memory and disk are MiB, because a byte count is neither readable in a payload nor
// something anyone wants to type.
export interface SessionResources {
  // size names one of the VM sizes. When set, the VM's CPU and memory come from that size.
  size?: string;
  // cpu is a core count. A VM rounds up to whole vCPUs.
  cpu?: number;
  memory_mb?: number;
  // disk_mb caps the writable layer. Only the VM runtime applies it.
  disk_mb?: number;
}

// SessionHost is the node a session lives on, as tagged by the aggregated list.
export interface SessionHost {
  stable_node_id: string;
  hostname: string;
  dns_name?: string;
}

// SessionListResponse is the aggregated GET /api/sessions payload: sessions across all your nodes,
// this node's identity, and any peers whose deepcove API didn't answer in time.
export interface SessionListResponse {
  sessions: Session[];
  self?: SessionHost;
  unreachable?: SessionHost[];
}

export interface CreateSessionRequest {
  agent: string;
  name?: string;
  env?: Record<string, string>;
  mounts?: MountRequest[];
  // Empty falls back to agent registry defaults.
  image?: string;
  setup_script?: string;
  // Which node hosts the session (a node's stable_node_id). Empty/this node = create here; another
  // node's id proxies the create there. Only meaningful with multiple nodes.
  node_id?: string;
  // Which sandbox runtime hosts the session ("applevf", "cloudhv", "sandboxless"). Empty uses the node's
  // default, and is refused on a node that has none.
  runtime?: string;
  // Omitted, or with any field zero, leaves that cap to the runtime.
  resources?: SessionResources;
  // Put the session on this node's shared machine instead of building it one. The machine owns the
  // mounts, its runtime and its size, so those are ignored.
  on_workspace_vm?: boolean;
  // Omitted means the node's default.
  login_mode?: LoginMode;
  // The session's folder under the workspace root, created if missing. Empty means one named after the
  // session. Workspace sessions only.
  work_dir?: string;
}

// LLMProxy is where this node's sessions send LLM requests. An empty url means straight to the
// provider, with the key inside the sandbox.
export interface LLMProxy {
  url: string;
}

// SharedLoginStatus mirrors internal/sharedlogin.Status, plus the node's login settings.
export interface SharedLoginStatus {
  logged_in: boolean;
  // How new sessions on this node log in, unless the user picks otherwise when creating one.
  default_login_mode: LoginMode;
  has_api_key: boolean;
  // The saved key's last four characters, empty when it is too short to spare them.
  api_key_ending: string;
  // Can be empty even when logged in, if the agent never said which account it is.
  account: string;
  waiting: { account: string; sandbox_id: string } | null;
}

// WorkspaceVM mirrors internal/session.WorkspaceVM: this node's shared machine, one per node. Sessions
// on it work in their own folder under host_root.
export interface WorkspaceVM {
  host_root: string;
  // Runs once when the machine is built. Empty means the agent's default.
  setup_script: string;
  // Where that folder appears inside the VM, and the root every session's folder lives under.
  guest_root: string;
  image: string;
  runtime: string;
  resources: SessionResources;
  // Absent until the machine has been built, which happens on first use.
  sandbox_id?: string;
  created_at: string; // ISO-8601
  updated_at: string; // ISO-8601
}

// WorkspaceVMRequest is the PUT body: empty fields take node defaults.
export interface WorkspaceVMRequest {
  host_root: string;
  setup_script?: string;
  // Empty means /workspace.
  guest_root?: string;
  runtime?: string;
  image?: string;
  resources?: SessionResources;
}

// MountRequest mirrors internal/session.MountRequest. host_path must
// be absolute on the deepcove host; container_path must be absolute
// inside the sandbox. /workspace is the conventional target (your
// code, where the agent starts); /root is reserved. Mode defaults to
// "rw" if omitted.
export interface MountRequest {
  host_path: string;
  container_path: string;
  mode?: 'rw' | 'ro';
}

// FsEntry / FsListResponse mirror internal/session. GET /api/fs/list
// lists subdirectories of a host path (scoped to $HOME) for the host-path
// autocomplete + Browse modal. parent is "" at the home root.
export interface FsEntry {
  name: string;
  path: string;
}
export interface FsListResponse {
  path: string;
  parent: string;
  entries: FsEntry[];
}

// ConnectStatus mirrors internal/connect.Status, the Tailscale "Connect" wizard
// state. phase: stopped → starting/needs_login (login_url set) → running (url set;
// http until https_enabled, then tls). user_id is an int64 (display only).
export interface ConnectOwner {
  user_id: number;
  login_name: string;
  display_name: string;
  profile_pic_url?: string;
}

// Me mirrors GET /api/me: the person using the dashboard, as Tailscale knows them.
export interface Me {
  login_name: string;
  display_name: string;
  profile_pic_url?: string;
}

// NodeInfo is one of the user's deepcove machines from GET /api/nodes (this one or a tailnet peer).
export interface NodeInfo {
  stable_node_id: string;
  hostname: string;
  dns_name: string;
  os?: string;
  online: boolean;
  self: boolean;
}
export interface ConnectStatus {
  phase: 'stopped' | 'starting' | 'needs_login' | 'running';
  login_url?: string;
  url?: string;
  tls: boolean;
  magicdns_enabled: boolean;
  https_enabled: boolean;
  // The tailnet this node is on, so the UI can spell out which tailnet a viewer must be connected to.
  tailnet_name?: string;
  name?: string; // this node's MagicDNS name
  ips?: string[]; // this node's tailnet IPs (v4, v6)
  owner?: ConnectOwner;
}

// official_image is empty until a prebuilt deepcove image is published. UI uses that
// to gate the "Official deepcove image" radio with a "coming soon" callout.
export interface Agent {
  name: string;
  display_name: string;
  // login_modes are the ways this agent may be logged in, safest first.
  login_modes: LoginMode[];
  // login_hint says how to log in inside a session.
  login_hint: string;
  // api_key_example is what the vendor's API keys start with.
  api_key_example: string;
  sandboxed: SandboxedAgent;
}

// in_session: its own login. shared: the user's login, kept by deepcove. api_key: the user's key, kept by
// deepcove. llm_proxy: the node's LLM proxy.
export type LoginMode = 'in_session' | 'shared' | 'api_key' | 'llm_proxy';

// How the agent is built into a guest, for the runtimes that have one. The script that starts it stays
// on the node, along with everything about installing it on a machine with no guest.
export interface SandboxedAgent {
  default_image: string;
  official_image: string;
  setup: string;
  env: Record<string, string>;
}

export interface SessionPreview {
  // Raw `tmux capture-pane -e` output with ANSI escapes. The client
  // feeds this into an unmounted xterm.js Terminal + SerializeAddon
  // to produce HTML, matching what the live xterm.js view would
  // render. Dimensions are derived from the bytes themselves.
  ansi: string;
  // This could not be captured now, so it is the last picture we had, or nothing.
  stale?: boolean;
}

// TmuxWindow mirrors internal/session.TmuxWindow.
export interface TmuxWindow {
  name: string;
  index: number;
}

// CreateTmuxWindowRequest mirrors internal/session.CreateTmuxWindowRequest.
// Name is optional, and the server auto-generates ("shell", "shell-2", ...)
// when blank.
export interface CreateTmuxWindowRequest {
  name?: string;
}

// Wire shape of an API error body. `code` is a stable machine-readable
// tag (e.g. "runtime_unavailable", "conflict") the client maps to
// friendly copy + actions; absent on older/generic errors.
export interface ApiError {
  error: string;
  code?: string;
}

// Stable error codes mirrored from internal/session/handlers.go. Keep
// in sync, because renames here without a server change break the mapping.
export type ApiErrorCode = 'invalid_input' | 'runtime_unavailable' | 'not_found' | 'conflict';

// GET /api/preflight returns environment readiness the dashboard reads on load
// and the New Session form reads before allowing create. Mirrors
// internal/session.PreflightResponse.
export interface Preflight {
  // Which runtimes this node can actually host a session on. A VM runtime is offered only where the
  // platform supports it and it is set up, so the form has to ask rather than assume.
  runtimes: RuntimesInfo;
}

// RuntimesInfo is one node's runtime menu, as reported by its preflight.
export interface RuntimesInfo {
  available: string[];
  // default is what a create request gets when it does not name a runtime. Empty when the node has no
  // runtime it will pick for you: No sandbox is all it has, or it has nothing.
  default: string;
  // unavailable maps a runtime this node has a driver for to why it cannot host a session right now,
  // e.g. a hypervisor that stopped answering. Absent from the map means usable. Being registered says the
  // driver exists, not that the backend is up, and the backend can come up without restarting the node.
  unavailable?: Record<string, string>;
  // missing maps a runtime this node knows about but cannot provide to a reason code. Unlike
  // unavailable, there is no driver at all and something has to be installed first.
  missing?: Record<string, string>;
  // session_vm_sizes are the sizes a session VM can have, each with how many fit on this machine.
  session_vm_sizes?: SessionVMSizeChoice[];
  // workspace_vm_sizes are the sizes the workspace VM can have, each with about how many agents it runs.
  workspace_vm_sizes?: WorkspaceVMSizeChoice[];
  default_vm_size?: string;
  // workspace_roots maps a runtime that makes a folder per session to where those folders live. Only a
  // runtime with no sandbox has one.
  workspace_roots?: Record<string, string>;
  // agents_to_install maps a runtime to the agents that machine does not have yet.
  agents_to_install?: Record<string, string[]>;
}

export interface VMSize {
  name: string;
  cpu: number;
  memory_mb: number;
}

export interface SessionVMSizeChoice extends VMSize {
  // Absent when the machine's memory could not be measured.
  fits_on_this_host?: number;
}

export interface WorkspaceVMSizeChoice extends VMSize {
  agents_it_runs: number;
}

export type BootstrapStatus = 'installing' | 'ready' | 'errored';

export interface BootstrapResponse {
  status: BootstrapStatus;
  log: string;
}

// RuntimesViewResponse mirrors internal/session.RuntimesViewResponse: GET /api/runtimes, this node's
// runtimes and what each is actually hosting.
export interface RuntimesViewResponse {
  runtimes: RuntimeView[];
}

// RuntimeView is one runtime on this node. Reachability, and what the driver says it is hosting joined
// against the session table. The join is the point: the two drift, and seeing where is how you tell a
// leaked sandbox from a session whose sandbox is already gone.
export interface RuntimeView {
  name: string;
  // default is true for the runtime a create request lands on when it names none.
  default: boolean;
  reachable: boolean;
  version?: string;
  // message is human-facing guidance when reachable is false.
  message?: string;
  // missing is the reason code when this node cannot provide the runtime at all, so there is no driver
  // to ask anything of. reachable is false and everything below is empty.
  missing?: string;
  // list_supported is false for a driver that cannot enumerate its sandboxes. That is a capability
  // gap, not an error, and sandboxes/orphaned then mean nothing rather than "none".
  list_supported: boolean;
  // list_error is set when listing was supported but failed, so an empty list is never read as
  // "nothing is running".
  list_error?: string;
  sandboxes: SandboxView[];
  orphaned: OrphanedSession[];
}

// SandboxView is one sandbox the driver is hosting, joined to the sessions living on it.
export interface SandboxView {
  sandbox_id: string;
  state: 'running' | 'starting' | 'stopped' | 'unknown';
  // The machine is up but not answering. A restart is what fixes it.
  unresponsive?: boolean;
  image?: string;
  // What the driver actually gave it. All zero means the driver applied its own defaults or cannot
  // report them.
  resources: SessionResources;
  created_at: string; // ISO-8601
  // The host folders this sandbox shares, as the driver reports them.
  mounts?: SandboxMount[];
  // The sessions on this sandbox; a shared machine has several. Empty means it leaked: still allocated
  // for finished work.
  sessions: SandboxSession[];
  // This node's shared machine. Allowed to have no sessions on it, and never a leak.
  is_workspace_vm?: boolean;
}

// SandboxMount is one host folder shared into a sandbox, and where it lands inside it.
export interface SandboxMount {
  host_path: string;
  guest_path: string;
  read_only?: boolean;
}

// SandboxSession is one session on a sandbox, enough to name it and link to it.
export interface SandboxSession {
  id: string;
  name: string;
  status: SessionStatus;
}

// OrphanedSession is a session whose sandbox is gone. The useful action is deleting the session, since
// there is no sandbox left to reattach to.
export interface OrphanedSession {
  session_id: string;
  session_name: string;
  sandbox_id?: string;
  status: SessionStatus;
}
