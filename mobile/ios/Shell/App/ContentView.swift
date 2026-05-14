// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 dlf-dds contributors.

import SwiftUI
import UniformTypeIdentifiers

struct ContentView: View {
    @EnvironmentObject private var tunnel: TunnelManager
    @State private var showingImporter = false
    @State private var importError: String?

    // Bundle-capability detection lives in the gomobile bridge once Worker A
    // exposes it (76N adds `inner_mesh_setup` + `mobile_cert` to the CBOR
    // schema; the SDK will surface them as a JSON blob the UI parses). Until
    // then, an imported v0.1.x bundle reports wg-cp0 only — which is the
    // correct default behaviour for the v0.2 baseline and lets us land the
    // mode-selector UI now without waiting on the foundation track.
    private var bundleCaps: BundleCapabilities {
        BundleStore.hasBundle
            ? BundleCapabilities(supportsWgCp0: true, supportsInnerMesh: false)
            : .empty
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 24) {
                    statusCards
                    modeCard
                    bundleCard
                    actionButtons
                }
                .padding()
            }
            .navigationTitle("goat-client")
            .fileImporter(
                isPresented: $showingImporter,
                allowedContentTypes: [.data, UTType(filenameExtension: "cbor") ?? .data],
                allowsMultipleSelection: false
            ) { result in
                handleImport(result)
            }
            .alert("Bundle import failed",
                   isPresented: Binding(get: { importError != nil }, set: { if !$0 { importError = nil } }),
                   actions: { Button("OK") { importError = nil } },
                   message: { Text(importError ?? "") })
        }
    }

    // MARK: - Status cards

    @ViewBuilder
    private var statusCards: some View {
        VStack(spacing: 12) {
            if tunnel.mode.hasWgCp0 {
                TunnelStatusCard(title: "wg-cp0 outer", subtitle: "Silent control plane",
                                 state: tunnel.wgCp0Tunnel)
            }
            if tunnel.mode.hasInnerMesh {
                TunnelStatusCard(title: "Inner mesh", subtitle: "netbird peer overlay",
                                 state: tunnel.innerMeshTunnel)
            }
            if let last = tunnel.lastErrorText {
                Text(last)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(4)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    // MARK: - Mode card

    private var modeCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Operating mode")
                .font(.headline)

            let available = bundleCaps.availableModes
            if available.isEmpty {
                Text("Import a bundle to choose a mode. v0.1.x bundles default to wg-cp0-only.")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            } else if available.count == 1 {
                HStack(spacing: 8) {
                    Image(systemName: "lock.fill")
                        .foregroundStyle(.secondary)
                    Text(available[0].displayName)
                        .font(.body.weight(.medium))
                }
                Text(available[0].blurb)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                Picker("Mode", selection: Binding(
                    get: { tunnel.mode },
                    set: { newValue in Task { await tunnel.selectMode(newValue) } }
                )) {
                    ForEach(available) { mode in
                        Text(mode.displayName).tag(mode)
                    }
                }
                .pickerStyle(.segmented)

                Text(tunnel.mode.blurb)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding()
        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 12))
    }

    // MARK: - Bundle card

    private var bundleCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Onboarding bundle")
                .font(.headline)
            if BundleStore.hasBundle {
                Text("Bundle imported.")
                    .foregroundStyle(.secondary)
                Button("Replace bundle…") { showingImporter = true }
                Button("Clear bundle", role: .destructive) {
                    BundleStore.clear()
                    ModeStore.reset()
                    tunnel.refreshBundleState()
                }
            } else {
                Text("No bundle imported yet. Tap below to pick a `.cbor` bundle from Files / iCloud Drive / a sandbox lab share.")
                    .foregroundStyle(.secondary)
                Button("Import bundle…") { showingImporter = true }
                    .buttonStyle(.borderedProminent)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding()
        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 12))
    }

    private var actionButtons: some View {
        HStack(spacing: 12) {
            Button {
                Task { await tunnel.connect() }
            } label: {
                Label("Connect", systemImage: "play.circle.fill")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .disabled(!BundleStore.hasBundle || tunnel.status == .connecting || tunnel.status == .connected)

            Button {
                Task { await tunnel.disconnect() }
            } label: {
                Label("Disconnect", systemImage: "stop.circle.fill")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.bordered)
            .disabled(tunnel.status == .disconnected)
        }
    }

    private func handleImport(_ result: Result<[URL], Error>) {
        switch result {
        case .failure(let err):
            importError = err.localizedDescription
        case .success(let urls):
            guard let url = urls.first else { return }
            // The fileImporter hands us a security-scoped URL; we need to
            // start/stop the scope to read across the document-picker boundary.
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            do {
                let data = try Data(contentsOf: url)
                guard data.count >= 64 else {
                    importError = "Selected file is too small (\(data.count) bytes); not a valid CBOR-signed bundle."
                    return
                }
                try BundleStore.write(data)
                // Clamp the mode to what this bundle supports — a stale
                // `combined` selection from a previous netbird-capable
                // bundle should not survive importing a wg-cp0-only one.
                let caps = BundleCapabilities(supportsWgCp0: true, supportsInnerMesh: false)
                if !caps.availableModes.contains(tunnel.mode), let fallback = caps.availableModes.first {
                    Task { await tunnel.selectMode(fallback) }
                }
                tunnel.refreshBundleState()
            } catch {
                importError = error.localizedDescription
            }
        }
    }
}

/// A single tunnel's status card. Two of these render in `combined` mode;
/// one each in `wg-cp0-only` and `netbird-only` modes.
struct TunnelStatusCard: View {
    let title: String
    let subtitle: String
    let state: TunnelCardState

    var body: some View {
        HStack(spacing: 16) {
            Circle()
                .fill(stateColor)
                .frame(width: 14, height: 14)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.subheadline.weight(.semibold))
                Text(subtitle)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            Text(state.label)
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding()
        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 12))
    }

    private var stateColor: Color {
        switch state {
        case .connected:  return .green
        case .connecting: return .orange
        case .error:      return .red
        case .idle:       return .gray
        case .disabled:   return Color.gray.opacity(0.3)
        }
    }
}
