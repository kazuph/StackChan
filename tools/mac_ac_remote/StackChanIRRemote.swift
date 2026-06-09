import AppKit
import Foundation

struct LearnedRemote: Codable, Equatable {
    let manufacturer: String
    let protocolName: String
    let description: String
    var lastSeen: TimeInterval
}

final class RemoteWindowController: NSObject {
    private let windowSize = NSSize(width: 700, height: 500)
    private let contentWidth: CGFloat = 420
    private let sidebarWidth: CGFloat = 210
    private let backgroundColor = NSColor.white
    private let foregroundColor = NSColor(calibratedWhite: 0.12, alpha: 1.0)
    private let secondaryTextColor = NSColor(calibratedWhite: 0.38, alpha: 1.0)
    private let panelBorderColor = NSColor(calibratedWhite: 0.72, alpha: 1.0)
    private let window: NSWindow
    private let repoRoot = URL(fileURLWithPath: "/Users/kazuph/src/github.com/kazuph/StackChan")
    private var helperURL: URL {
        let bundled = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/stackchan-ir-tool")
        if FileManager.default.isExecutableFile(atPath: bundled.path) {
            return bundled
        }
        return URL(fileURLWithPath: "/tmp/stackchan-ir-tool")
    }
    private let stateURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".config/stackchan-swiftbar/ac_remote_ui_state.json")
    private let statusLabel = NSTextField(labelWithString: "StackChan IR Remote")
    private let manufacturerLabel = NSTextField(labelWithString: "")
    private let protocolLabel = NSTextField(labelWithString: "")
    private let detectionTextView = NSTextView()
    private let tempLabel = NSTextField(labelWithString: "26 C")
    private let swingVLabel = NSTextField(labelWithString: "-")
    private let swingHLabel = NSTextField(labelWithString: "-")
    private let modeControl = NSSegmentedControl(labels: ["冷房", "除湿", "暖房", "自動"], trackingMode: .selectOne, target: nil, action: nil)
    private let fanControl = NSPopUpButton()
    private let powerSwitch = NSSwitch()
    private let remoteListStack = NSStackView()
    private var learnedRemotes: [LearnedRemote] = []
    private var temp = 26
    private var receiveTimer: DispatchSourceTimer?
    private var receiveMonitor: Process?
    private var receiveMonitorBuffer = ""
    private let pollQueue = DispatchQueue(label: "stackchan.ir.remote.poll", qos: .utility)
    private var appActivity: NSObjectProtocol?
    private let receiveMonitorEnabled = false
    private var activeProtocol = ""
    private var decodeAfterTimestamp = Date().timeIntervalSince1970
    private var lastDecodedFrameCount = 0
    private var pollGeneration = 0
    private var pollCount = 0
    private var isResettingReceiver = false
    private var decodeInFlight = false
    private var statusHoldUntil = Date.distantPast
    private var lastSpokenFrameCount = 0
    private var isApplyingRemoteState = false
    private var isSendInFlight = false
    private var queuedSendPower: String?

    override init() {
        window = NSWindow(
            contentRect: NSRect(origin: .zero, size: windowSize),
            styleMask: [.titled, .closable, .miniaturizable],
            backing: .buffered,
            defer: false
        )
        super.init()
        if let lightAppearance = NSAppearance(named: .aqua) {
            NSApp.appearance = lightAppearance
            window.appearance = lightAppearance
        }
        window.title = "StackChan IR Remote"
        window.backgroundColor = backgroundColor
        window.isOpaque = true
        window.minSize = windowSize
        window.maxSize = windowSize
        window.center()
        buildUI()
        appActivity = ProcessInfo.processInfo.beginActivity(
            options: [.userInitiatedAllowingIdleSystemSleep],
            reason: "Keep StackChan IR polling active"
        )
    }

    func show() {
        logDebug("show window")
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        logDebug("window visible=\(window.isVisible) key=\(window.isKeyWindow) miniaturized=\(window.isMiniaturized)")
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) {
            logDebug("window delayed visible=\(self.window.isVisible) key=\(self.window.isKeyWindow) miniaturized=\(self.window.isMiniaturized)")
        }
    }

    private func buildUI() {
        let outer = NSStackView()
        outer.orientation = .horizontal
        outer.alignment = .top
        outer.spacing = 14
        outer.edgeInsets = NSEdgeInsets(top: 10, left: 20, bottom: 10, right: 14)
        outer.translatesAutoresizingMaskIntoConstraints = false
        outer.wantsLayer = true
        outer.layer?.backgroundColor = backgroundColor.cgColor
        window.contentView = outer

        let root = NSStackView()
        root.orientation = .vertical
        root.alignment = .centerX
        root.spacing = 11
        root.translatesAutoresizingMaskIntoConstraints = false
        root.wantsLayer = true
        root.layer?.backgroundColor = backgroundColor.cgColor
        root.widthAnchor.constraint(equalToConstant: contentWidth).isActive = true
        outer.addArrangedSubview(root)

        let receiveRow = NSView()
        receiveRow.translatesAutoresizingMaskIntoConstraints = false
        receiveRow.widthAnchor.constraint(equalToConstant: contentWidth).isActive = true
        receiveRow.heightAnchor.constraint(equalToConstant: 40).isActive = true

        let receiveLabel = label("受光中")
        receiveLabel.translatesAutoresizingMaskIntoConstraints = false
        receiveRow.addSubview(receiveLabel)

        let reset = NSButton(title: "↻", target: self, action: #selector(resetDetectedState))
        reset.toolTip = "判定表示をリセット"
        reset.bezelStyle = .rounded
        reset.controlSize = .large
        reset.font = .systemFont(ofSize: 22, weight: .bold)
        reset.translatesAutoresizingMaskIntoConstraints = false
        receiveRow.addSubview(reset)
        NSLayoutConstraint.activate([
            receiveLabel.leadingAnchor.constraint(equalTo: receiveRow.leadingAnchor),
            receiveLabel.centerYAnchor.constraint(equalTo: receiveRow.centerYAnchor),
            reset.trailingAnchor.constraint(equalTo: receiveRow.trailingAnchor),
            reset.centerYAnchor.constraint(equalTo: receiveRow.centerYAnchor),
            reset.widthAnchor.constraint(equalToConstant: 42),
            reset.heightAnchor.constraint(equalToConstant: 34),
        ])
        root.addArrangedSubview(receiveRow)

        statusLabel.font = .systemFont(ofSize: 13)
        statusLabel.textColor = secondaryTextColor
        statusLabel.lineBreakMode = .byTruncatingMiddle
        statusLabel.maximumNumberOfLines = 2
        statusLabel.alignment = .left
        statusLabel.widthAnchor.constraint(equalToConstant: contentWidth).isActive = true

        let manufacturerRow = NSView()
        manufacturerRow.translatesAutoresizingMaskIntoConstraints = false
        manufacturerRow.widthAnchor.constraint(equalToConstant: contentWidth).isActive = true
        manufacturerRow.heightAnchor.constraint(equalToConstant: 62).isActive = true

        manufacturerLabel.font = .systemFont(ofSize: 33, weight: .black)
        manufacturerLabel.alignment = .center
        manufacturerLabel.textColor = foregroundColor
        manufacturerLabel.lineBreakMode = .byTruncatingMiddle
        manufacturerLabel.maximumNumberOfLines = 1
        manufacturerLabel.translatesAutoresizingMaskIntoConstraints = false
        manufacturerRow.addSubview(manufacturerLabel)

        protocolLabel.font = .systemFont(ofSize: 15, weight: .semibold)
        protocolLabel.alignment = .center
        protocolLabel.textColor = secondaryTextColor
        protocolLabel.lineBreakMode = .byTruncatingMiddle
        protocolLabel.maximumNumberOfLines = 1
        protocolLabel.translatesAutoresizingMaskIntoConstraints = false
        manufacturerRow.addSubview(protocolLabel)

        NSLayoutConstraint.activate([
            manufacturerLabel.centerXAnchor.constraint(equalTo: manufacturerRow.centerXAnchor),
            manufacturerLabel.topAnchor.constraint(equalTo: manufacturerRow.topAnchor, constant: 2),
            manufacturerLabel.leadingAnchor.constraint(greaterThanOrEqualTo: manufacturerRow.leadingAnchor),
            manufacturerLabel.trailingAnchor.constraint(lessThanOrEqualTo: manufacturerRow.trailingAnchor),
            protocolLabel.centerXAnchor.constraint(equalTo: manufacturerRow.centerXAnchor),
            protocolLabel.topAnchor.constraint(equalTo: manufacturerLabel.bottomAnchor, constant: 2),
            protocolLabel.leadingAnchor.constraint(greaterThanOrEqualTo: manufacturerRow.leadingAnchor),
            protocolLabel.trailingAnchor.constraint(lessThanOrEqualTo: manufacturerRow.trailingAnchor),
        ])
        root.addArrangedSubview(manufacturerRow)

        root.addArrangedSubview(separator())

        let powerRow = row()
        powerRow.addArrangedSubview(label("運転"))
        powerSwitch.state = .on
        powerSwitch.target = self
        powerSwitch.action = #selector(powerChanged)
        powerSwitch.controlSize = .large
        powerRow.addArrangedSubview(powerSwitch)
        powerRow.addArrangedSubview(balanceSpacer())
        root.addArrangedSubview(powerRow)

        modeControl.selectedSegment = 0
        modeControl.controlSize = .large
        modeControl.font = .systemFont(ofSize: 17, weight: .semibold)
        modeControl.heightAnchor.constraint(equalToConstant: 34).isActive = true
        modeControl.widthAnchor.constraint(equalToConstant: 280).isActive = true
        modeControl.target = self
        modeControl.action = #selector(modeChanged)
        root.addArrangedSubview(labeled("モード", modeControl))

        let tempRow = row()
        tempRow.addArrangedSubview(label("温度"))
        let down = NSButton(title: "-", target: self, action: #selector(tempDown))
        styleButton(down, width: 56)
        let up = NSButton(title: "+", target: self, action: #selector(tempUp))
        styleButton(up, width: 56)
        tempLabel.alignment = .center
        tempLabel.font = .monospacedDigitSystemFont(ofSize: 34, weight: .bold)
        tempLabel.widthAnchor.constraint(equalToConstant: 110).isActive = true
        tempRow.addArrangedSubview(down)
        tempRow.addArrangedSubview(tempLabel)
        tempRow.addArrangedSubview(up)
        tempRow.addArrangedSubview(balanceSpacer())
        root.addArrangedSubview(tempRow)

        fanControl.addItems(withTitles: ["自動", "静音", "弱", "中", "強"])
        stylePopup(fanControl)
        fanControl.target = self
        fanControl.action = #selector(fanChanged)
        root.addArrangedSubview(labeled("風量", fanControl))

        let swingRow = row()
        let swingTitle = label("風向き")
        swingVLabel.font = .systemFont(ofSize: 17, weight: .semibold)
        swingVLabel.textColor = secondaryTextColor
        swingVLabel.widthAnchor.constraint(greaterThanOrEqualToConstant: 50).isActive = true
        swingHLabel.font = .systemFont(ofSize: 17, weight: .semibold)
        swingHLabel.textColor = secondaryTextColor
        swingHLabel.widthAnchor.constraint(greaterThanOrEqualToConstant: 50).isActive = true
        swingRow.addArrangedSubview(swingTitle)
        swingRow.addArrangedSubview(valueCaption("上下"))
        swingRow.addArrangedSubview(swingVLabel)
        swingRow.addArrangedSubview(valueCaption("左右"))
        swingRow.addArrangedSubview(swingHLabel)
        swingRow.addArrangedSubview(balanceSpacer())
        root.addArrangedSubview(swingRow)

        root.addArrangedSubview(separator())
        root.addArrangedSubview(statusLabel)
        root.addArrangedSubview(detectionView())

        let buttonRow = row()
        let send = NSButton(title: "現在値を送信", target: self, action: #selector(sendCurrent))
        styleButton(send, width: 155)
        let check = NSButton(title: "接続確認", target: self, action: #selector(checkStatus))
        styleButton(check, width: 105)
        buttonRow.addArrangedSubview(send)
        buttonRow.addArrangedSubview(check)
        root.addArrangedSubview(buttonRow)

        let note = NSTextField(labelWithString: "送信IRはWeb APIで生成し、本体IR RemoteモジュールのRMT送信から出します。")
        note.font = .systemFont(ofSize: 13)
        note.textColor = secondaryTextColor
        note.lineBreakMode = .byWordWrapping
        note.maximumNumberOfLines = 2
        note.widthAnchor.constraint(equalToConstant: contentWidth).isActive = true
        root.addArrangedSubview(note)

        outer.addArrangedSubview(sidebarView())
        loadLastState()
        startReceivePolling()
    }

    private func sidebarView() -> NSView {
        let sidebar = NSStackView()
        sidebar.orientation = .vertical
        sidebar.alignment = .leading
        sidebar.spacing = 8
        sidebar.translatesAutoresizingMaskIntoConstraints = false
        sidebar.widthAnchor.constraint(equalToConstant: sidebarWidth).isActive = true

        let title = NSTextField(labelWithString: "検知済み")
        title.font = .systemFont(ofSize: 18, weight: .bold)
        title.textColor = foregroundColor
        title.widthAnchor.constraint(equalToConstant: sidebarWidth).isActive = true
        sidebar.addArrangedSubview(title)

        let caption = NSTextField(labelWithString: "受光して判定できたメーカー/型番")
        caption.font = .systemFont(ofSize: 12)
        caption.textColor = secondaryTextColor
        caption.lineBreakMode = .byWordWrapping
        caption.maximumNumberOfLines = 2
        caption.widthAnchor.constraint(equalToConstant: sidebarWidth).isActive = true
        sidebar.addArrangedSubview(caption)

        let panel = NSView()
        panel.wantsLayer = true
        panel.layer?.backgroundColor = backgroundColor.cgColor
        panel.layer?.borderColor = panelBorderColor.cgColor
        panel.layer?.borderWidth = 1
        panel.translatesAutoresizingMaskIntoConstraints = false
        panel.widthAnchor.constraint(equalToConstant: sidebarWidth).isActive = true
        panel.heightAnchor.constraint(equalToConstant: 400).isActive = true

        let list = NSScrollView()
        list.hasVerticalScroller = true
        list.hasHorizontalScroller = false
        list.drawsBackground = true
        list.backgroundColor = backgroundColor
        list.translatesAutoresizingMaskIntoConstraints = false
        panel.addSubview(list)
        NSLayoutConstraint.activate([
            list.leadingAnchor.constraint(equalTo: panel.leadingAnchor, constant: 6),
            list.trailingAnchor.constraint(equalTo: panel.trailingAnchor, constant: -6),
            list.topAnchor.constraint(equalTo: panel.topAnchor, constant: 6),
            list.bottomAnchor.constraint(equalTo: panel.bottomAnchor, constant: -6),
        ])

        remoteListStack.orientation = .vertical
        remoteListStack.alignment = .leading
        remoteListStack.spacing = 6
        remoteListStack.translatesAutoresizingMaskIntoConstraints = false
        remoteListStack.widthAnchor.constraint(equalToConstant: sidebarWidth - 28).isActive = true
        list.documentView = remoteListStack
        sidebar.addArrangedSubview(panel)
        return sidebar
    }

    private func row() -> NSStackView {
        let view = NSStackView()
        view.orientation = .horizontal
        view.alignment = .centerY
        view.spacing = 10
        return view
    }

    private func labeled(_ label: String, _ control: NSView) -> NSStackView {
        let view = row()
        let text = self.label(label)
        view.addArrangedSubview(text)
        view.addArrangedSubview(control)
        view.addArrangedSubview(balanceSpacer())
        return view
    }

    private func label(_ text: String) -> NSTextField {
        let field = NSTextField(labelWithString: text)
        field.font = .systemFont(ofSize: 16, weight: .semibold)
        field.textColor = foregroundColor
        field.alignment = .right
        field.widthAnchor.constraint(equalToConstant: 82).isActive = true
        return field
    }

    private func balanceSpacer() -> NSView {
        let spacer = NSView()
        spacer.widthAnchor.constraint(equalToConstant: 82).isActive = true
        spacer.heightAnchor.constraint(equalToConstant: 1).isActive = true
        return spacer
    }

    private func valueCaption(_ text: String) -> NSTextField {
        let field = NSTextField(labelWithString: text)
        field.font = .systemFont(ofSize: 15, weight: .medium)
        field.textColor = secondaryTextColor
        return field
    }

    private func separator() -> NSBox {
        let box = NSBox()
        box.boxType = .separator
        box.widthAnchor.constraint(equalToConstant: contentWidth).isActive = true
        return box
    }

    private func styleButton(_ button: NSButton, width: CGFloat) {
        button.bezelStyle = .rounded
        button.controlSize = .large
        button.font = .systemFont(ofSize: 16, weight: .semibold)
        button.widthAnchor.constraint(equalToConstant: width).isActive = true
        button.heightAnchor.constraint(equalToConstant: 34).isActive = true
    }

    private func stylePopup(_ popup: NSPopUpButton) {
        popup.controlSize = .large
        popup.font = .systemFont(ofSize: 16, weight: .semibold)
        popup.widthAnchor.constraint(equalToConstant: 280).isActive = true
        popup.heightAnchor.constraint(equalToConstant: 34).isActive = true
    }

    private func detectionView() -> NSView {
        let scrollView = NSScrollView()
        scrollView.hasVerticalScroller = true
        scrollView.hasHorizontalScroller = false
        scrollView.borderType = .bezelBorder
        scrollView.backgroundColor = backgroundColor
        scrollView.drawsBackground = true
        scrollView.wantsLayer = true
        scrollView.layer?.backgroundColor = backgroundColor.cgColor
        scrollView.layer?.borderColor = panelBorderColor.cgColor
        scrollView.layer?.borderWidth = 1
        scrollView.translatesAutoresizingMaskIntoConstraints = false
        scrollView.widthAnchor.constraint(equalToConstant: contentWidth).isActive = true
        scrollView.heightAnchor.constraint(equalToConstant: 86).isActive = true

        detectionTextView.isEditable = false
        detectionTextView.isSelectable = true
        detectionTextView.drawsBackground = true
        detectionTextView.backgroundColor = backgroundColor
        detectionTextView.font = .systemFont(ofSize: 14)
        detectionTextView.textColor = secondaryTextColor
        detectionTextView.textContainerInset = NSSize(width: 8, height: 6)
        detectionTextView.textContainer?.widthTracksTextView = true
        detectionTextView.textContainer?.containerSize = NSSize(width: contentWidth, height: CGFloat.greatestFiniteMagnitude)
        detectionTextView.isHorizontallyResizable = false
        detectionTextView.isVerticallyResizable = true
        detectionTextView.autoresizingMask = [.width]
        detectionTextView.string = ""
        scrollView.documentView = detectionTextView
        return scrollView
    }

    private func startReceivePolling() {
        guard !isSendInFlight else {
            logDebug("receive polling start skipped during send generation=\(pollGeneration)")
            return
        }
        statusLabel.stringValue = "受光待機中: 新しい判定を待っています"
        receiveTimer?.cancel()
        receiveTimer = nil
        stopReceiveMonitor()
        decodeInFlight = false
        receiveMonitorBuffer = ""
        guard receiveMonitorEnabled else {
            statusLabel.stringValue = "受光監視中: 新しい判定を待っています"
            let timer = DispatchSource.makeTimerSource(queue: pollQueue)
            timer.schedule(deadline: .now() + 0.1, repeating: .seconds(1), leeway: .milliseconds(250))
            timer.setEventHandler { [weak self] in
                self?.requestPollDecode()
            }
            receiveTimer = timer
            timer.resume()
            logDebug("ir monitor disabled; fallback poll timer started generation=\(pollGeneration)")
            return
        }

        let task = Process()
        task.executableURL = helperURL
        task.currentDirectoryURL = repoRoot
        task.arguments = [
            "watch-mcp-ir",
            "--interval", "0.4",
            "--max-age", "180",
        ]
        let pipe = Pipe()
        task.standardOutput = pipe
        task.standardError = pipe
        let generation = pollGeneration
        pipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            if data.isEmpty {
                return
            }
            guard let chunk = String(data: data, encoding: .utf8) else {
                return
            }
            DispatchQueue.main.async {
                self?.handleMonitorChunk(chunk, generation: generation)
            }
        }
        task.terminationHandler = { [weak self] process in
            DispatchQueue.main.async {
                guard let self, self.receiveMonitor === process, !self.isResettingReceiver else {
                    return
                }
                logDebug("ir monitor exited status=\(process.terminationStatus)")
                self.receiveMonitor = nil
                self.statusLabel.stringValue = "受光monitor再接続中..."
                DispatchQueue.main.asyncAfter(deadline: .now() + 1.0) {
                    self.startReceivePolling()
                }
            }
        }
        do {
            try task.run()
            receiveMonitor = task
            logDebug("ir monitor started generation=\(pollGeneration)")
        } catch {
            statusLabel.stringValue = "ERROR: monitor起動失敗 \(compact(error.localizedDescription))"
            logDebug("ir monitor start failed: \(error.localizedDescription)")
        }
    }

    private func stopReceiveMonitor() {
        guard let monitor = receiveMonitor else {
            return
        }
        receiveMonitor = nil
        monitor.standardOutput = nil
        monitor.standardError = nil
        if monitor.isRunning {
            monitor.terminate()
        }
    }

    private func pauseReceivePollingForSend() {
        pollGeneration += 1
        receiveTimer?.cancel()
        receiveTimer = nil
        stopReceiveMonitor()
        decodeInFlight = false
        logDebug("receive polling paused for send generation=\(pollGeneration)")
    }

    func shutdown() {
        receiveTimer?.cancel()
        receiveTimer = nil
        stopReceiveMonitor()
        if let activity = appActivity {
            ProcessInfo.processInfo.endActivity(activity)
            appActivity = nil
        }
    }

    private func handleMonitorChunk(_ chunk: String, generation: Int) {
        guard generation == pollGeneration else {
            return
        }
        receiveMonitorBuffer += chunk
        while let newline = receiveMonitorBuffer.firstIndex(of: "\n") {
            let line = String(receiveMonitorBuffer[..<newline]).trimmingCharacters(in: .whitespacesAndNewlines)
            receiveMonitorBuffer.removeSubrange(...newline)
            if !line.isEmpty {
                handleMonitorLine(line, generation: generation)
            }
        }
    }

    private func handleMonitorLine(_ line: String, generation: Int) {
        logDebug("ir monitor line \(compact(line))")
        guard generation == pollGeneration,
              let data = line.data(using: .utf8),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return
        }
        if let event = json["event"] as? String {
            if event == "heartbeat" {
                let frameCount = json["frame_count"] as? Int ?? lastDecodedFrameCount
                statusLabel.stringValue = "受光監視中: frame \(frameCount)"
            } else if event == "error" {
                statusLabel.stringValue = "受光monitor: \(compact(json["error"] as? String ?? "error"))"
            }
            return
        }
        applyDecodeResult(json)
    }

    private func applyDecodeResult(_ json: [String: Any]) {
        let frameCount = json["frame_count"] as? Int ?? self.lastDecodedFrameCount
        self.lastDecodedFrameCount = frameCount
        if (json["ok"] as? Bool) == false {
            if (json["short_frame"] as? Bool) == true {
                let captured = json["captured_durations"] as? Int ?? 0
                let protocolName = json["protocol"] as? String ?? "UNKNOWN"
                self.statusLabel.stringValue = "受光待機中: 短いIRを無視しました"
                self.detectionTextView.string = "受光: 短いIRを無視\nprotocol: \(protocolName), durations: \(captured)\nエアコン用の長い信号を待っています。"
                return
            }
            let reason = self.compact((json["error"] as? String) ?? "library did not decode this frame")
            let captured = json["captured_durations"] as? Int ?? 0
            self.statusLabel.stringValue = "受光中: frame \(frameCount) は未判定"
            self.detectionTextView.string = "判定: 未判定\nframe: \(frameCount), durations: \(captured)\n\(reason)"
            return
        }
        let manufacturer = json["manufacturer"] as? String ?? "Unknown"
        let decodedProtocol = json["protocol"] as? String ?? "UNKNOWN"
        let description = json["description"] as? String ?? ""
        let supported = json["supported_send"] as? Bool ?? false
        let age = json["age_sec"] as? Double ?? 0
        self.setDetectedHeader(manufacturer: manufacturer, protocolName: decodedProtocol)
        self.detectionTextView.string = decodeSummary(manufacturer: manufacturer, protocolName: decodedProtocol, description: description, age: age)
        self.applyDescription(description)
        self.announceDecodeResult(json, manufacturer: manufacturer, protocolName: decodedProtocol, frameCount: frameCount)
        if supported {
            self.activeProtocol = decodedProtocol
            self.addLearnedRemote(manufacturer: manufacturer, protocolName: decodedProtocol, description: description)
            self.statusLabel.stringValue = "OK: 受光結果を送信対象に反映しました"
        } else {
            self.statusLabel.stringValue = "検知: \(decodedProtocol)（この画面からの送信は未対応）"
        }
        self.saveLastState()
    }

    @objc private func resetDetectedState() {
        decodeAfterTimestamp = Date().timeIntervalSince1970
        lastDecodedFrameCount = 0
        lastSpokenFrameCount = 0
        pollGeneration += 1
        receiveTimer?.cancel()
        receiveTimer = nil
        stopReceiveMonitor()
        decodeInFlight = false
        isResettingReceiver = true
        activeProtocol = ""
        manufacturerLabel.stringValue = ""
        protocolLabel.stringValue = ""
        detectionTextView.string = ""
        swingVLabel.stringValue = "-"
        swingHLabel.stringValue = "-"
        statusLabel.stringValue = "受光リセット中..."
        runHelperProcess(args: ["reset-receiver"]) { exitCode, output in
            self.isResettingReceiver = false
            self.lastDecodedFrameCount = 0
            self.lastSpokenFrameCount = 0
            self.statusLabel.stringValue = exitCode == 0 ? "受光待機中: 新しい判定を待っています" : "ERROR: \(self.compact(output))"
            self.startReceivePolling()
        }
        saveLastState()
    }

    @objc private func tempDown() {
        temp = max(16, temp - 1)
        updateTemp()
        sendAfterUserControlChange(reason: "tempDown")
    }

    @objc private func tempUp() {
        temp = min(31, temp + 1)
        updateTemp()
        sendAfterUserControlChange(reason: "tempUp")
    }

    @objc private func modeChanged() {
        updateTemp()
        sendAfterUserControlChange(reason: "modeChanged")
    }

    @objc private func fanChanged() {
        sendAfterUserControlChange(reason: "fanChanged")
    }

    @objc private func powerChanged() {
        sendAfterUserControlChange(reason: "powerChanged")
    }

    private func updateTemp() {
        tempLabel.stringValue = modeControl.selectedSegment == 3 ? "自動" : "\(temp) C"
    }

    @objc private func checkStatus() {
        runHelper(args: ["status"])
    }

    @objc private func sendCurrent() {
        logDebug("sendCurrent tapped power=\(powerSwitch.state == .on) protocol=\(activeProtocol)")
        sendWithCurrentState(power: powerSwitch.state == .on ? "on" : "off")
    }

    private func sendWithCurrentState(power: String) {
        if activeProtocol.isEmpty {
            selectDefaultLearnedRemote()
        }
        guard !activeProtocol.isEmpty else {
            statusLabel.stringValue = "ERROR: 先にIRを受光してください"
            logDebug("send aborted: no active protocol and no learned remotes")
            return
        }
        if isSendInFlight {
            queuedSendPower = power
            statusLabel.stringValue = "送信中... 次の操作を反映します"
            detectionTextView.string = [
                "送信待ち:",
                "protocol: \(activeProtocol)",
                "power: \(power)",
                "mode: \(modeValue())",
                "temp: \(temp)C",
                "fan: \(fanValue())",
            ].joined(separator: "\n")
            logDebug("send queued protocol=\(activeProtocol) power=\(power) mode=\(modeValue()) temp=\(temp) fan=\(fanValue())")
            saveLastState()
            return
        }
        isSendInFlight = true
        pauseReceivePollingForSend()
        logDebug("send start protocol=\(activeProtocol) power=\(power) mode=\(modeValue()) temp=\(temp) fan=\(fanValue())")
        runHelper(args: [
            "send",
            "--protocol", activeProtocol,
            "--power", power,
            "--mode", modeValue(),
            "--temp", "\(temp)",
            "--fan", fanValue(),
        ])
        saveLastState()
    }

    private func sendAfterUserControlChange(reason: String) {
        guard !isApplyingRemoteState else {
            return
        }
        logDebug("remote control changed reason=\(reason) power=\(powerSwitch.state == .on) protocol=\(activeProtocol)")
        sendWithCurrentState(power: powerSwitch.state == .on ? "on" : "off")
    }

    private func selectDefaultLearnedRemote() {
        guard let remote = learnedRemotes.first else {
            return
        }
        applyLearnedRemote(remote)
    }

    private func selectActiveOrDefaultLearnedRemote() {
        if let remote = learnedRemotes.first(where: { $0.protocolName == activeProtocol }) {
            applyLearnedRemote(remote)
            return
        }
        selectDefaultLearnedRemote()
    }

    private func applyLearnedRemote(_ remote: LearnedRemote) {
        activeProtocol = remote.protocolName
        setDetectedHeader(manufacturer: remote.manufacturer, protocolName: remote.protocolName)
        detectionTextView.string = "選択: manufacturer: \(remote.manufacturer)\nprotocol: \(remote.protocolName)\n\(remote.description)"
        applyDescription(remote.description)
        statusLabel.stringValue = "選択中: \(remote.manufacturer) / \(remote.protocolName)"
        renderLearnedRemotes()
    }

    private func modeValue() -> String {
        switch modeControl.selectedSegment {
        case 1: return "dry"
        case 2: return "heat"
        case 3: return "auto"
        default: return "cool"
        }
    }

    private func fanValue() -> String {
        switch fanControl.indexOfSelectedItem {
        case 1: return "silent"
        case 2: return "low"
        case 3: return "medium"
        case 4: return "high"
        default: return "auto"
        }
    }

    private func runHelper(args: [String]) {
        statusLabel.stringValue = "送信中..."
        statusHoldUntil = Date().addingTimeInterval(25)
        logDebug("helper start args=\(args.joined(separator: " "))")
        runHelperProcess(args: args) { exitCode, output in
            logDebug("helper done exit=\(exitCode) output=\(self.compact(output))")
            self.statusHoldUntil = Date().addingTimeInterval(25)
            if args.first == "send" {
                self.updateSendResult(exitCode: exitCode, output: output)
            } else {
                self.statusLabel.stringValue = exitCode == 0 ? "OK: \(self.compact(output))" : "ERROR: \(self.compact(output))"
            }
        }
    }

    private func updateSendResult(exitCode: Int32, output: String) {
        if exitCode != 0 {
            statusLabel.stringValue = "ERROR: IR送信に失敗しました"
            detectionTextView.string = "送信失敗:\n\(compact(output))"
            finishSendCycle()
            return
        }
        let data = output.data(using: .utf8) ?? Data()
        let json = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
        let protocolName = json?["protocol"] as? String ?? activeProtocol
        let durations = json?["durations"] as? Int ?? 0
        let frequency = json?["frequency"] as? Int ?? 0
        let routeLog = json?["route_log"] as? [String] ?? []
        let mode = modeValue()
        lastDecodedFrameCount = 0
        statusLabel.stringValue = "OK: IR送信しました \(protocolName)"
        var lines = [
            "送信完了:",
            "protocol: \(protocolName)",
            "power: \(powerSwitch.state == .on ? "on" : "off")",
            "mode: \(mode)",
            "temp: \(temp)C",
            "fan: \(fanValue())",
            "durations: \(durations)",
            "frequency: \(frequency)Hz",
            "",
            "経路ログ:"
        ].filter { !$0.isEmpty }
        lines.append(contentsOf: routeLog.map { "- \($0)" })
        detectionTextView.string = lines.joined(separator: "\n")
        finishSendCycle()
    }

    private func finishSendCycle() {
        isSendInFlight = false
        guard let nextPower = queuedSendPower else {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.8) {
                guard !self.isSendInFlight else {
                    logDebug("receive polling restart skipped; send in-flight generation=\(self.pollGeneration)")
                    return
                }
                self.startReceivePolling()
            }
            return
        }
        queuedSendPower = nil
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) {
            self.sendWithCurrentState(power: nextPower)
        }
    }

    private func requestPollDecode() {
        DispatchQueue.main.async {
            self.pollDecodeOnMain()
        }
    }

    private func pollDecodeOnMain() {
        if isResettingReceiver {
            logDebug("poll skip resetting generation=\(pollGeneration)")
            return
        }
        let generation = pollGeneration
        if decodeInFlight {
            logDebug("poll skip in-flight generation=\(generation) afterFrame=\(lastDecodedFrameCount)")
            return
        }
        decodeInFlight = true
        pollCount += 1
        let currentPoll = pollCount
        let afterFrame = lastDecodedFrameCount
        logDebug("poll start #\(currentPoll) generation=\(generation) afterFrame=\(afterFrame)")
        runHelperProcess(
            args: ["decode-mcp-latest", "--after-frame-count", "\(afterFrame)", "--max-age", "180"],
            timeoutSeconds: 10
        ) { exitCode, output in
            self.decodeInFlight = false
            logDebug("poll done #\(currentPoll) exit=\(exitCode) output=\(self.compact(output))")
            if generation != self.pollGeneration || self.isResettingReceiver {
                logDebug("poll discard #\(currentPoll) stale generation=\(generation) current=\(self.pollGeneration)")
                return
            }
            if exitCode != 0 {
                if self.isDeviceSessionError(output) {
                    self.statusLabel.stringValue = "接続待ち: StackChan本体セッションがありません"
                    self.detectionTextView.string = "接続: StackChan本体がブリッジに接続していません。\n本体のWi-Fiまたは再起動後のWebSocket接続を待っています。"
                    return
                }
                if self.isWaitingForFreshDecode(output) {
                    if Date() >= self.statusHoldUntil {
                        self.statusLabel.stringValue = "受光待機中: poll #\(currentPoll) / 新しい判定を待っています"
                    }
                    return
                }
                self.detectionTextView.string = "判定: 受光しましたが、ライブラリで判定できませんでした。\n\(self.humanDecodeError(output))"
                return
            }
            guard let data = output.data(using: .utf8),
                  let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
                self.detectionTextView.string = "判定: JSON parse error"
                return
            }
            let frameCount = json["frame_count"] as? Int ?? self.lastDecodedFrameCount
            self.lastDecodedFrameCount = frameCount
            if Date() < self.statusHoldUntil {
                logDebug("poll discard #\(currentPoll) while status is held afterFrame=\(frameCount)")
                return
            }
            if (json["ok"] as? Bool) == false {
                if (json["short_frame"] as? Bool) == true {
                    let captured = json["captured_durations"] as? Int ?? 0
                    let protocolName = json["protocol"] as? String ?? "UNKNOWN"
                    self.statusLabel.stringValue = "受光待機中: 短いIRを無視しました"
                    self.detectionTextView.string = "受光: 短いIRを無視\nprotocol: \(protocolName), durations: \(captured)\nエアコン用の長い信号を待っています。"
                    return
                }
                let reason = self.compact((json["error"] as? String) ?? "library did not decode this frame")
                let captured = json["captured_durations"] as? Int ?? 0
                self.statusLabel.stringValue = "受光中: frame \(frameCount) は未判定"
                self.detectionTextView.string = "判定: 未判定\nframe: \(frameCount), durations: \(captured)\n\(reason)"
                return
            }
            let manufacturer = json["manufacturer"] as? String ?? "Unknown"
            let decodedProtocol = json["protocol"] as? String ?? "UNKNOWN"
            let description = json["description"] as? String ?? ""
            let supported = json["supported_send"] as? Bool ?? false
            let age = json["age_sec"] as? Double ?? 0
            self.setDetectedHeader(manufacturer: manufacturer, protocolName: decodedProtocol)
            self.detectionTextView.string = self.decodeSummary(manufacturer: manufacturer, protocolName: decodedProtocol, description: description, age: age)
            self.applyDescription(description)
            self.announceDecodeResult(json, manufacturer: manufacturer, protocolName: decodedProtocol, frameCount: frameCount)
            if supported {
                self.activeProtocol = decodedProtocol
                self.addLearnedRemote(manufacturer: manufacturer, protocolName: decodedProtocol, description: description)
                self.statusLabel.stringValue = "OK: 受光結果を送信対象に反映しました"
            } else {
                self.statusLabel.stringValue = "検知: \(decodedProtocol)（この画面からの送信は未対応）"
            }
            self.saveLastState()
        }
    }

    private func setDetectedHeader(manufacturer: String, protocolName: String) {
        manufacturerLabel.stringValue = manufacturer
        protocolLabel.stringValue = protocolName.isEmpty ? "" : "Protocol: \(protocolName)"
    }

    private func decodeSummary(manufacturer: String, protocolName: String, description: String, age: Double) -> String {
        return "判定: \(String(format: "%.1f", age))秒前\nmanufacturer: \(manufacturer)\nprotocol: \(protocolName)\n\(description)"
    }

    private func announceDecodeResult(_ json: [String: Any], manufacturer: String, protocolName: String, frameCount: Int) {
        guard frameCount > 0, frameCount != lastSpokenFrameCount else {
            return
        }
        let age = json["age_sec"] as? Double ?? 999
        guard age <= 8 else {
            logDebug("speak decode skipped old frame=\(frameCount) age=\(age)")
            return
        }
        lastSpokenFrameCount = frameCount
        guard let payload = irSpeechPayloadJSON(json, manufacturer: manufacturer, protocolName: protocolName, frameCount: frameCount) else {
            logDebug("announce decode skipped: payload serialization failed frame=\(frameCount)")
            return
        }
        logDebug("announce decode frame=\(frameCount) manufacturer=\(manufacturer) protocol=\(protocolName)")
        runHelperProcess(args: ["announce-ir", "--payload", payload], timeoutSeconds: 8) { exitCode, output in
            if exitCode != 0 {
                logDebug("announce decode failed exit=\(exitCode) output=\(self.compact(output))")
            } else {
                logDebug("announce decode queued output=\(self.compact(output))")
            }
        }
    }

    private func irSpeechPayloadJSON(_ json: [String: Any], manufacturer: String, protocolName: String, frameCount: Int) -> String? {
        var payload: [String: Any] = [
            "manufacturer": manufacturer,
            "protocol": protocolName,
            "frame_count": frameCount,
        ]
        if let decoded = json["decoded"] as? [String: Any] {
            payload["decoded"] = decoded
        }
        guard JSONSerialization.isValidJSONObject(payload),
              let data = try? JSONSerialization.data(withJSONObject: payload, options: []) else {
            return nil
        }
        return String(data: data, encoding: .utf8)
    }

    private func addLearnedRemote(manufacturer: String, protocolName: String, description: String) {
        guard !manufacturer.isEmpty, !protocolName.isEmpty, protocolName.uppercased() != "UNKNOWN" else {
            return
        }
        let remote = LearnedRemote(
            manufacturer: manufacturer,
            protocolName: protocolName,
            description: description,
            lastSeen: Date().timeIntervalSince1970
        )
        learnedRemotes.removeAll { $0.manufacturer == manufacturer && $0.protocolName == protocolName }
        learnedRemotes.insert(remote, at: 0)
        renderLearnedRemotes()
    }

    private func renderLearnedRemotes() {
        remoteListStack.arrangedSubviews.forEach { view in
            remoteListStack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }

        if learnedRemotes.isEmpty {
            let empty = NSTextField(labelWithString: "まだありません")
            empty.font = .systemFont(ofSize: 14)
            empty.textColor = secondaryTextColor
            empty.widthAnchor.constraint(equalToConstant: sidebarWidth - 32).isActive = true
            remoteListStack.addArrangedSubview(empty)
            return
        }

        for (index, remote) in learnedRemotes.enumerated() {
            remoteListStack.addArrangedSubview(remoteRow(remote, index: index))
        }
    }

    private func remoteRow(_ remote: LearnedRemote, index: Int) -> NSView {
        let row = NSView()
        row.translatesAutoresizingMaskIntoConstraints = false
        row.widthAnchor.constraint(equalToConstant: sidebarWidth - 32).isActive = true
        row.heightAnchor.constraint(equalToConstant: 58).isActive = true
        row.wantsLayer = true
        row.layer?.backgroundColor = (remote.protocolName == activeProtocol ? NSColor(calibratedWhite: 0.93, alpha: 1.0) : backgroundColor).cgColor
        row.layer?.borderColor = panelBorderColor.cgColor
        row.layer?.borderWidth = remote.protocolName == activeProtocol ? 1 : 0
        row.layer?.cornerRadius = 6

        let select = NSButton(title: "", target: self, action: #selector(selectLearnedRemote(_:)))
        select.isBordered = false
        select.tag = index
        select.translatesAutoresizingMaskIntoConstraints = false
        row.addSubview(select)

        let manufacturer = NSTextField(labelWithString: remote.manufacturer)
        manufacturer.font = .systemFont(ofSize: 16, weight: .bold)
        manufacturer.textColor = foregroundColor
        manufacturer.lineBreakMode = .byTruncatingTail
        manufacturer.translatesAutoresizingMaskIntoConstraints = false
        row.addSubview(manufacturer)

        let protocolName = NSTextField(labelWithString: remote.protocolName)
        protocolName.font = .systemFont(ofSize: 12, weight: .medium)
        protocolName.textColor = secondaryTextColor
        protocolName.lineBreakMode = .byTruncatingMiddle
        protocolName.translatesAutoresizingMaskIntoConstraints = false
        row.addSubview(protocolName)

        let delete = NSButton(title: "×", target: self, action: #selector(deleteLearnedRemote(_:)))
        delete.bezelStyle = .rounded
        delete.controlSize = .small
        delete.font = .systemFont(ofSize: 13, weight: .bold)
        delete.tag = index
        delete.translatesAutoresizingMaskIntoConstraints = false
        row.addSubview(delete)

        NSLayoutConstraint.activate([
            select.leadingAnchor.constraint(equalTo: row.leadingAnchor),
            select.trailingAnchor.constraint(equalTo: row.trailingAnchor),
            select.topAnchor.constraint(equalTo: row.topAnchor),
            select.bottomAnchor.constraint(equalTo: row.bottomAnchor),
            manufacturer.leadingAnchor.constraint(equalTo: row.leadingAnchor, constant: 8),
            manufacturer.trailingAnchor.constraint(equalTo: delete.leadingAnchor, constant: -6),
            manufacturer.topAnchor.constraint(equalTo: row.topAnchor, constant: 8),
            protocolName.leadingAnchor.constraint(equalTo: manufacturer.leadingAnchor),
            protocolName.trailingAnchor.constraint(equalTo: manufacturer.trailingAnchor),
            protocolName.topAnchor.constraint(equalTo: manufacturer.bottomAnchor, constant: 2),
            delete.trailingAnchor.constraint(equalTo: row.trailingAnchor, constant: -4),
            delete.centerYAnchor.constraint(equalTo: row.centerYAnchor),
            delete.widthAnchor.constraint(equalToConstant: 28),
            delete.heightAnchor.constraint(equalToConstant: 24),
        ])
        return row
    }

    @objc private func selectLearnedRemote(_ sender: NSButton) {
        guard learnedRemotes.indices.contains(sender.tag) else {
            return
        }
        let remote = learnedRemotes[sender.tag]
        activeProtocol = remote.protocolName
        setDetectedHeader(manufacturer: remote.manufacturer, protocolName: remote.protocolName)
        detectionTextView.string = "選択: manufacturer: \(remote.manufacturer)\nprotocol: \(remote.protocolName)\n\(remote.description)"
        applyDescription(remote.description)
        statusLabel.stringValue = "選択中: \(remote.manufacturer) / \(remote.protocolName)"
        renderLearnedRemotes()
        saveLastState()
    }

    @objc private func deleteLearnedRemote(_ sender: NSButton) {
        guard learnedRemotes.indices.contains(sender.tag) else {
            return
        }
        let removed = learnedRemotes.remove(at: sender.tag)
        if activeProtocol == removed.protocolName {
            activeProtocol = ""
            manufacturerLabel.stringValue = ""
            protocolLabel.stringValue = ""
            detectionTextView.string = ""
            statusLabel.stringValue = "削除しました。新しい判定を待っています"
        }
        renderLearnedRemotes()
        saveLastState()
    }

    private func applyDescription(_ description: String) {
        isApplyingRemoteState = true
        defer { isApplyingRemoteState = false }

        if description.contains("Power: Off") {
            powerSwitch.state = .off
        } else if description.contains("Power: On") {
            powerSwitch.state = .on
        }

        if let modeName = fieldName(in: description, field: "Mode") {
            applyModeName(modeName)
        } else if description.contains("(Cool)") {
            modeControl.selectedSegment = 0
        }

        if let tempRange = description.range(of: #"Temp: (\d+)C"#, options: .regularExpression) {
            let value = String(description[tempRange]).replacingOccurrences(of: "Temp: ", with: "").replacingOccurrences(of: "C", with: "")
            if let decodedTemp = Int(value), (16...31).contains(decodedTemp) {
                temp = decodedTemp
            }
        }
        updateTemp()

        if let fanName = fieldName(in: description, field: "Fan") {
            applyFanName(fanName)
        }

        swingVLabel.stringValue = fieldName(in: description, field: "Swing\\(V\\)") ?? fieldName(in: description, field: "Swing") ?? "-"
        swingHLabel.stringValue = fieldName(in: description, field: "Swing\\(H\\)") ?? "-"
    }

    private func fieldName(in description: String, field: String) -> String? {
        let namedPattern = "\(field):\\s*\\d+\\s*\\(([^)]+)\\)"
        if let range = description.range(of: namedPattern, options: .regularExpression) {
            let fragment = String(description[range])
            guard let open = fragment.firstIndex(of: "("),
                  let close = fragment.lastIndex(of: ")"),
                  open < close else {
                return nil
            }
            return String(fragment[fragment.index(after: open)..<close])
        }

        let valuePattern = "\(field):\\s*([^,]+)"
        guard let range = description.range(of: valuePattern, options: .regularExpression) else {
            return nil
        }
        let fragment = String(description[range])
        guard let colon = fragment.firstIndex(of: ":") else {
            return nil
        }
        return String(fragment[fragment.index(after: colon)...]).trimmingCharacters(in: .whitespaces)
    }

    private func applyModeName(_ name: String) {
        switch name.lowercased() {
        case "cool":
            modeControl.selectedSegment = 0
        case "dry":
            modeControl.selectedSegment = 1
        case "heat":
            modeControl.selectedSegment = 2
        case "auto":
            modeControl.selectedSegment = 3
        default:
            break
        }
    }

    private func applyFanName(_ name: String) {
        switch name.lowercased() {
        case "auto":
            fanControl.selectItem(at: 0)
        case "silent", "quiet":
            fanControl.selectItem(at: 1)
        case "low", "min":
            fanControl.selectItem(at: 2)
        case "medium", "med":
            fanControl.selectItem(at: 3)
        case "high", "max":
            fanControl.selectItem(at: 4)
        default:
            break
        }
    }

    private func saveLastState() {
        let remotes = learnedRemotes.map { remote in
            [
                "manufacturer": remote.manufacturer,
                "protocol": remote.protocolName,
                "description": remote.description,
                "last_seen": remote.lastSeen,
            ] as [String: Any]
        }
        let state: [String: Any] = [
            "protocol": activeProtocol,
            "manufacturer": manufacturerLabel.stringValue,
            "status": statusLabel.stringValue,
            "detection": detectionTextView.string,
            "power": powerSwitch.state == .on,
            "mode_index": modeControl.selectedSegment,
            "temp": temp,
            "fan_index": fanControl.indexOfSelectedItem,
            "swing_v": swingVLabel.stringValue,
            "swing_h": swingHLabel.stringValue,
            "remotes": remotes,
        ]
        do {
            try FileManager.default.createDirectory(at: stateURL.deletingLastPathComponent(), withIntermediateDirectories: true)
            let data = try JSONSerialization.data(withJSONObject: state, options: [.prettyPrinted, .sortedKeys])
            try data.write(to: stateURL)
        } catch {
            logDebug("save state failed: \(error.localizedDescription)")
        }
    }

    private func loadLastState() {
        guard let data = try? Data(contentsOf: stateURL),
              let state = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return
        }
        activeProtocol = state["protocol"] as? String ?? activeProtocol
        if let remotes = state["remotes"] as? [[String: Any]] {
            learnedRemotes = remotes.compactMap { item in
                guard let manufacturer = item["manufacturer"] as? String,
                      let protocolName = item["protocol"] as? String,
                      !protocolName.isEmpty,
                      protocolName.uppercased() != "UNKNOWN" else {
                    return nil
                }
                return LearnedRemote(
                    manufacturer: manufacturer,
                    protocolName: protocolName,
                    description: item["description"] as? String ?? "",
                    lastSeen: item["last_seen"] as? TimeInterval ?? Date().timeIntervalSince1970
                )
            }
            renderLearnedRemotes()
        }
        selectActiveOrDefaultLearnedRemote()
        powerSwitch.state = (state["power"] as? Bool ?? true) ? .on : .off
        modeControl.selectedSegment = state["mode_index"] as? Int ?? modeControl.selectedSegment
        temp = state["temp"] as? Int ?? temp
        updateTemp()
        fanControl.selectItem(at: state["fan_index"] as? Int ?? fanControl.indexOfSelectedItem)
    }

    private func runHelperProcess(args: [String], timeoutSeconds: TimeInterval = 12, completion: @escaping (Int32, String) -> Void) {
        let task = Process()
        task.executableURL = helperURL
        task.currentDirectoryURL = repoRoot
        task.arguments = args

        let pipe = Pipe()
        task.standardOutput = pipe
        task.standardError = pipe

        DispatchQueue.global(qos: .userInitiated).async {
            do {
                try task.run()
                let deadline = Date().addingTimeInterval(timeoutSeconds)
                while task.isRunning && Date() < deadline {
                    Thread.sleep(forTimeInterval: 0.05)
                }
                var timedOut = false
                if task.isRunning {
                    timedOut = true
                    task.terminate()
                    Thread.sleep(forTimeInterval: 0.2)
                }
                task.waitUntilExit()
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                let output = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                DispatchQueue.main.async {
                    completion(timedOut ? 124 : task.terminationStatus, timedOut ? "helper timeout after \(Int(timeoutSeconds))s: \(output)" : output)
                }
            } catch {
                DispatchQueue.main.async {
                    completion(1, error.localizedDescription)
                }
            }
        }
    }

    private func compact(_ text: String) -> String {
        let oneLine = flatten(text)
        if oneLine.count <= 240 {
            return oneLine
        }
        return String(oneLine.prefix(237)) + "..."
    }

    private func flatten(_ text: String) -> String {
        text.replacingOccurrences(of: "\n", with: " ")
    }

    private func humanDecodeError(_ text: String) -> String {
        let line = flatten(text).replacingOccurrences(of: "ERROR: ", with: "")
        if isDeviceSessionError(text) {
            return "StackChan本体がブリッジに接続していません。"
        }
        if isWaitingForFreshDecode(text) {
            return "新しいエアコン信号を待っています。最後の判定状態は保持しています。"
        }
        if line.contains("MULTIBRACKETS") {
            return "直近フレームはエアコン信号として判定できませんでした。リモコンをIRユニットに向けて、もう一度押してください。"
        }
        if line.contains("no recent IR frame") {
            return "まだ十分な長さのIR信号を受け取っていません。"
        }
        if line.contains("could not be decoded") {
            return "直近フレームは受け取りましたが、デコードに失敗しました。"
        }
        return line
    }

    private func isWaitingForFreshDecode(_ text: String) -> Bool {
        text.contains("no decodable AC frame in the last")
            || text.contains("no IR captures yet")
            || text.contains("no newer IR frame yet")
            || text.contains("no recent IR frame")
    }

    private func isDeviceSessionError(_ text: String) -> Bool {
        text.contains("HTTP 409") && text.contains("no active device session")
    }
}

private func logDebug(_ message: String) {
    let url = URL(fileURLWithPath: "/tmp/StackChanIRRemote.debug.log")
    let line = "\(Date()) \(message)\n"
    if let data = line.data(using: .utf8) {
        if FileManager.default.fileExists(atPath: url.path),
           let handle = try? FileHandle(forWritingTo: url) {
            handle.seekToEndOfFile()
            handle.write(data)
            try? handle.close()
        } else {
            try? data.write(to: url)
        }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var controller: RemoteWindowController?

    func applicationDidFinishLaunching(_ notification: Notification) {
        logDebug("applicationDidFinishLaunching")
        controller = RemoteWindowController()
        controller?.show()
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        true
    }

    func applicationWillTerminate(_ notification: Notification) {
        controller?.shutdown()
    }
}

let app = NSApplication.shared
let appDelegate = AppDelegate()
app.setActivationPolicy(.regular)
app.delegate = appDelegate
app.run()
