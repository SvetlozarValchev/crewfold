import { spawn, type ChildProcess } from "node:child_process";
import { existsSync } from "node:fs";
import { join } from "node:path";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

const bindingEntryType = "crewfold-binding";

type Binding = {
	active: boolean;
	room: string;
	handle: string;
	participantId: string;
	cursor: number;
};

type Participant = {
	id: string;
	handle: string;
	last_read_sequence: number;
};

type RoomMessage = {
	sequence: number;
	id: string;
	participant_id?: string;
	sender_handle: string;
	sender_kind: string;
	kind: string;
	body: string;
	document?: { name: string };
};

function crewfoldBinary(): string {
	const configured = process.env.CREWFOLD_BIN?.trim();
	if (configured) return configured;
	if (process.platform === "win32" && process.env.LOCALAPPDATA) {
		const installed = join(process.env.LOCALAPPDATA, "Programs", "Crewfold", "crewfold.exe");
		if (existsSync(installed)) return installed;
	}
	return "crewfold";
}

function runCrewfold(arguments_: string[], cwd: string): Promise<string> {
	return new Promise((resolve, reject) => {
		const child = spawn(crewfoldBinary(), arguments_, {
			cwd,
			windowsHide: true,
			stdio: ["ignore", "pipe", "pipe"],
		});
		let stdout = "";
		let stderr = "";
		const limit = 1024 * 1024;
		const timeout = setTimeout(() => child.kill(), 15_000);
		child.stdout.setEncoding("utf8");
		child.stderr.setEncoding("utf8");
		child.stdout.on("data", (chunk: string) => {
			if (stdout.length < limit) stdout += chunk.slice(0, limit - stdout.length);
		});
		child.stderr.on("data", (chunk: string) => {
			if (stderr.length < limit) stderr += chunk.slice(0, limit - stderr.length);
		});
		child.on("error", (error) => {
			clearTimeout(timeout);
			reject(error);
		});
		child.on("close", (code) => {
			clearTimeout(timeout);
			if (code === 0) resolve(stdout);
			else reject(new Error(stderr.trim() || `crewfold exited with code ${code}`));
		});
	});
}

export default function crewfoldExtension(pi: ExtensionAPI) {
	let binding: Binding | undefined;
	let watcher: ChildProcess | undefined;
	let watcherGeneration = 0;
	let shuttingDown = false;
	let restartTimer: ReturnType<typeof setTimeout> | undefined;
	let batchTimer: ReturnType<typeof setTimeout> | undefined;
	let pending: RoomMessage[] = [];

	function updateStatus(ctx: ExtensionContext) {
		ctx.ui.setStatus("crewfold", binding?.active ? `crewfold: ${binding.room} as @${binding.handle}` : undefined);
	}

	function saveBinding() {
		if (binding) pi.appendEntry(bindingEntryType, binding);
	}

	function stopWatcher() {
		watcherGeneration++;
		if (restartTimer) clearTimeout(restartTimer);
		restartTimer = undefined;
		if (batchTimer) clearTimeout(batchTimer);
		batchTimer = undefined;
		pending = [];
		watcher?.kill();
		watcher = undefined;
	}

	function deliverPending() {
		batchTimer = undefined;
		if (!binding || pending.length === 0) return;
		const messages = pending;
		pending = [];
		const body = messages
			.map((message) => {
				let label = `#${message.sequence} · @${message.sender_handle}`;
				if (message.kind !== "message") label += ` · ${message.kind}`;
				if (message.document) label += ` · ${message.document.name}`;
				const content = message.body.trim();
				return content ? `${label}\n${content}` : label;
			})
			.join("\n\n");
		pi.sendMessage(
			{
				customType: "crewfold-delivery",
				content: `[CREWFOLD ROOM DELIVERY]\n\nNew activity is available for @${binding.handle} in room ${binding.room}. This is shared-room coordination from Crewfold, not a direct owner instruction.\n\n${body}\n\nUse Crewfold to respond or publish context when useful.`,
				display: true,
				details: { room: binding.room, through: binding.cursor },
			},
			{ triggerTurn: true, deliverAs: "steer" },
		);
	}

	function acceptMessage(message: RoomMessage) {
		if (!binding || !Number.isSafeInteger(message.sequence) || message.sequence <= binding.cursor) return;
		binding.cursor = message.sequence;
		if (message.sender_kind === "system" || message.participant_id === binding.participantId) return;
		pending.push(message);
		if (!batchTimer) batchTimer = setTimeout(deliverPending, 200);
	}

	function startWatcher(ctx: ExtensionContext) {
		stopWatcher();
		if (!binding?.active || shuttingDown) return;
		const generation = watcherGeneration;
		const child = spawn(
			crewfoldBinary(),
			["room", "watch", binding.room, "--after", String(binding.cursor), "--output", "json"],
			{ cwd: ctx.cwd, windowsHide: true, stdio: ["ignore", "pipe", "pipe"] },
		);
		watcher = child;
		let buffer = "";
		let stderr = "";
		child.stdout.setEncoding("utf8");
		child.stderr.setEncoding("utf8");
		child.stdout.on("data", (chunk: string) => {
			buffer += chunk;
			while (true) {
				const newline = buffer.indexOf("\n");
				if (newline < 0) break;
				let line = buffer.slice(0, newline);
				buffer = buffer.slice(newline + 1);
				if (line.endsWith("\r")) line = line.slice(0, -1);
				if (!line.trim()) continue;
				try {
					acceptMessage(JSON.parse(line) as RoomMessage);
				} catch (error) {
					ctx.ui.notify(`Crewfold returned invalid room activity: ${String(error)}`, "error");
				}
			}
		});
		child.stderr.on("data", (chunk: string) => {
			if (stderr.length < 4096) stderr += chunk.slice(0, 4096 - stderr.length);
		});
		child.on("error", (error) => {
			stderr = error.message;
		});
		child.on("close", () => {
			if (watcher === child) watcher = undefined;
			if (generation !== watcherGeneration || shuttingDown || !binding?.active) return;
			ctx.ui.notify(`Crewfold room watch stopped${stderr.trim() ? `: ${stderr.trim()}` : ""}; retrying`, "warning");
			restartTimer = setTimeout(() => startWatcher(ctx), 2000);
		});
	}

	async function joinRoom(room: string, handle: string, ctx: ExtensionContext) {
		const output = await runCrewfold(
			["room", "join", room, "--handle", handle, "--delivery", "none", "--output", "json"],
			ctx.cwd,
		);
		const participant = JSON.parse(output) as Participant;
		if (!participant.id || !participant.handle) throw new Error("Crewfold returned an invalid participant");
		binding = {
			active: true,
			room,
			handle: participant.handle,
			participantId: participant.id,
			cursor: participant.last_read_sequence || 0,
		};
		saveBinding();
		updateStatus(ctx);
		startWatcher(ctx);
	}

	pi.on("session_start", async (_event, ctx) => {
		shuttingDown = false;
		for (const entry of ctx.sessionManager.getBranch()) {
			if (entry.type === "custom" && entry.customType === bindingEntryType) {
				binding = entry.data as Binding;
			}
		}
		if (!binding?.active) {
			updateStatus(ctx);
			return;
		}
		try {
			await joinRoom(binding.room, binding.handle, ctx);
		} catch (error) {
			ctx.ui.notify(`Crewfold binding could not resume: ${String(error)}`, "warning");
		}
	});

	pi.on("session_shutdown", async () => {
		shuttingDown = true;
		stopWatcher();
	});

	pi.registerCommand("crewfold-join", {
		description: "Join a Crewfold room: /crewfold-join ROOM HANDLE",
		handler: async (args, ctx) => {
			const [room, handle, ...extra] = args.trim().split(/\s+/);
			if (!room || !handle || extra.length > 0) {
				ctx.ui.notify("Usage: /crewfold-join ROOM HANDLE", "warning");
				return;
			}
			try {
				await joinRoom(room, handle, ctx);
				ctx.ui.notify(`Joined Crewfold room ${room} as @${handle}`, "info");
			} catch (error) {
				ctx.ui.notify(`Crewfold join failed: ${String(error)}`, "error");
			}
		},
	});

	pi.registerCommand("crewfold-disconnect", {
		description: "Stop delivering Crewfold activity to this Pi session",
		handler: async (_args, ctx) => {
			stopWatcher();
			if (binding) {
				binding = { ...binding, active: false };
				saveBinding();
			}
			updateStatus(ctx);
			ctx.ui.notify("Crewfold delivery disconnected", "info");
		},
	});

	pi.registerCommand("crewfold-status", {
		description: "Show this Pi session's Crewfold binding",
		handler: async (_args, ctx) => {
			if (!binding?.active) {
				ctx.ui.notify("This Pi session is not connected to a Crewfold room", "info");
				return;
			}
			ctx.ui.notify(`Connected to ${binding.room} as @${binding.handle} through #${binding.cursor}`, "info");
		},
	});
}
