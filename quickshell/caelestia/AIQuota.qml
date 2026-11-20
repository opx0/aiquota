pragma ComponentBehavior: Bound

import QtQuick
import QtQuick.Controls
import QtQuick.Effects
import QtQuick.Layouts
import QtCore
import Quickshell
import Quickshell.Io
import Caelestia.Config
import qs.components
import qs.services

Flickable {
    id: root

    property var providers: []
    property string error: "Loading cached quota..."
    property string executable: "aiquota"
    property bool rereadCache: false
    property bool readingCache: false
    property bool readerStarted: false
    property bool readerTimedOut: false
    property bool readerExited: false
    property bool readerStreamFinished: false
    property bool readerSucceeded: false
    property string readerOutput: ""
    property real maximumWidth: 480
    signal interactionRequested()
    property string selectedProviderId: ""
    readonly property var selectedProvider: providers.find(p => p.id === selectedProviderId) || null
    readonly property color cardColor: "#383835"
    readonly property color textColor: "#f5f4ed"
    readonly property color borderColor: Qt.rgba(240 / 255, 238 / 255, 230 / 255, 0.3)

    // Antigravity uses Google's published palette as a proxy.
    function providerPalette(id: string): var {
        switch (id) {
        case "codex": return { card: "#363636", text: "#ffffff", accent: "#10a37f", border: "#3e3e42" };
        case "antigravity": return { card: "#292c33", text: "#e8e8e8", accent: "#1a73e8", track: "#101114", border: Qt.rgba(232 / 255, 232 / 255, 232 / 255, 0.3) };
        case "claude": return { card: "#383835", text: "#f5f4ed", accent: "#c96442", track: "#101114", border: Qt.rgba(245 / 255, 244 / 255, 237 / 255, 0.3) };
        default: return { card: "#363636", text: "#ffffff", accent: "#ffffff", border: "#3e3e42" };
        }
    }

    function quotaState(percent: var): string {
        if (percent == null || !Number.isFinite(percent) || percent < 0 || percent > 100)
            return "unknown";
        const rounded = Math.round(percent);
        return rounded >= 60 ? "high" : rounded >= 30 ? "moderate" : "low";
    }

    function quotaColor(percent: var): color {
        switch (quotaState(percent)) {
        case "high": return "#4ade80";
        case "moderate": return "#fbbf24";
        case "low": return "#f87171";
        default: return "transparent";
        }
    }

    function quotaDescription(percent: var): string {
        const state = quotaState(percent);
        return state === "unknown" ? "remaining unknown" : Math.round(percent) + " percent remaining, " + state + " remaining quota";
    }

    function openProvider(id: string): void {
        selectedProviderId = id;
        contentY = 0;
        Qt.callLater(() => backButton.forceActiveFocus(Qt.OtherFocusReason));
    }

    function closeProvider(): void {
        const previousId = selectedProviderId;
        selectedProviderId = "";
        contentY = 0;
        if (previousId)
            Qt.callLater(() => {
                for (const item of providerGrid.children) {
                    if (item.objectName === "provider-card-" + previousId) {
                        item.focusCard();
                        return;
                    }
                }
            });
    }

    // Prefer a known percentage, then the shortest known duration. Stable
    // identifiers make ties independent of the order returned by a provider.
    function primaryWindow(windows: var): var {
        const known = (windows || []).filter(w => quotaState(w.remaining_percent) !== "unknown");
        known.sort((a, b) => {
            const ad = a.duration_minutes > 0 ? a.duration_minutes : Infinity;
            const bd = b.duration_minutes > 0 ? b.duration_minutes : Infinity;
            if (ad !== bd)
                return ad - bd;
            const ak = [a.model || "", a.bucket_id || "", a.id || "", a.label || ""].join("\u0000");
            const bk = [b.model || "", b.bucket_id || "", b.id || "", b.label || ""].join("\u0000");
            return ak < bk ? -1 : ak > bk ? 1 : 0;
        });
        return known.length ? known[0] : null;
    }

    function windowIdentity(window: var): string {
        if (!window)
            return "No known percentage";
        const name = windowName(window);
        const detail = windowDetail(window);
        const parts = [];
        for (const part of [window.model, window.bucket_id, name, detail, window.id]) {
            if (part && !parts.includes(part))
                parts.push(part);
        }
        return parts.join(" · ");
    }

    function windowDetail(window: var): string {
        const name = windowName(window);
        const label = window.label || "";
        return label && label.toLowerCase() !== name.toLowerCase() ? label : "";
    }

    function providerStatus(provider: var): string {
        if (provider.status === "healthy")
            return "Healthy";
        return statusText(provider.status || "");
    }

    function accountSummary(provider: var): string {
        const parts = [];
        if (provider.plan)
            parts.push(provider.plan);
        const c = provider.credits;
        if (c) {
            if (c.unlimited === true) parts.push("Unlimited credits");
            else if (c.balance != null) parts.push("Credits " + c.balance);
            else if (c.has_credits === true) parts.push("Credits available");
            else if (c.has_credits === false) parts.push("No credits");
        }
        return parts.join("  ·  ");
    }

    function amountText(window: var): string {
        const parts = [];
        const unit = window.unit ? " " + window.unit : "";
        if (window.used_amount != null)
            parts.push("Used " + window.used_amount + unit);
        if (window.remaining_amount != null)
            parts.push("Remaining " + window.remaining_amount + unit);
        if (window.allocation_limit != null)
            parts.push("Limit " + window.allocation_limit + unit);
        return parts.join("  ·  ");
    }

    function compactAmount(window: var): string {
        const unit = window.unit ? " " + window.unit : "";
        if (window.remaining_amount != null && window.allocation_limit != null)
            return window.remaining_amount + " / " + window.allocation_limit + unit + " left";
        if (window.remaining_amount != null)
            return window.remaining_amount + unit + " left";
        if (window.used_amount != null && window.allocation_limit != null)
            return window.used_amount + " / " + window.allocation_limit + unit + " used";
        if (window.used_amount != null)
            return "Used " + window.used_amount + unit;
        if (window.allocation_limit != null)
            return "Limit " + window.allocation_limit + unit;
        return "";
    }

    function readCache(): void {
        if (!readingCache) {
            readingCache = true;
            readerStarted = false;
            readerTimedOut = false;
            readerExited = false;
            readerStreamFinished = false;
            readerSucceeded = false;
            readerOutput = "";
            readerTimeout.restart();
            reader.command = [cacheExecutable(), "status", "--cached", "--json"];
            reader.running = true;
        } else
            rereadCache = true;
    }

    function cacheExecutable(): string {
        if (executable !== "aiquota")
            return executable;
        const home = Quickshell.env("HOME");
        if (!home)
            return executable;
        // QtCore returns a file URL, whereas Process requires a filesystem path.
        const local = String(StandardPaths.findExecutable("aiquota", [home + "/.local/bin"]));
        return local.startsWith("file://") ? decodeURIComponent(local.slice(7)) : executable;
    }

    function finishCacheRead(): void {
        readingCache = false;
        readerTimeout.stop();
        readerKillTimeout.stop();
        if (rereadCache) {
            rereadCache = false;
            Qt.callLater(readCache);
        }
    }

    function completeCacheRead(): void {
        if (!readingCache || !readerExited || !readerStreamFinished)
            return;
        if (!readerTimedOut && readerSucceeded) {
            try {
                const data = JSON.parse(readerOutput);
                if (data.schema_version !== 1 || !Array.isArray(data.providers))
                    throw new Error("Unsupported status schema");
                providers = data.providers;
                if (selectedProviderId && !selectedProvider)
                    closeProvider();
                error = "";
            } catch (e) {
                providers = [];
                closeProvider();
                error = "Cannot read AIQuota cached status.";
            }
        } else if (!readerTimedOut) {
            cacheUnavailable();
        }
        finishCacheRead();
    }

    function cacheUnavailable(): void {
        providers = [];
        closeProvider();
        error = "AIQuota is unavailable. Check that it is installed in ~/.local/bin or on PATH.";
    }

    function orderedProviders(providers: var): var {
        const order = { codex: 0, antigravity: 1, claude: 3 };
        return (providers || []).slice().sort((a, b) => (order[a.id] ?? 2) - (order[b.id] ?? 2));
    }

    function groups(windows: var): var {
        const result = [];
        const buckets = new Set((windows || []).filter(w => !w.model && w.bucket_id).map(w => w.bucket_id));
        for (const window of windows || []) {
            const name = window.model || (buckets.size > 1 ? window.bucket_id : "") || "";
            let group = result.find(g => g.name === name);
            if (!group) {
                group = { name: name, windows: [] };
                result.push(group);
            }
            group.windows.push(window);
        }
        for (const group of result) {
            group.windows.sort((a, b) => (a.duration_minutes || Infinity) - (b.duration_minutes || Infinity));
        }
        return result;
    }

    function windowName(window: var): string {
        const minutes = window.duration_minutes;
        if (minutes && minutes % 1440 === 0)
            return (minutes / 1440) + (minutes === 1440 ? " day" : " days");
        if (minutes && minutes % 60 === 0)
            return (minutes / 60) + (minutes === 60 ? " hour" : " hours");
        if (minutes)
            return minutes + " minutes";
        return window.label || window.id || "Quota";
    }

    function shortWindow(window: var): string {
        const minutes = window.duration_minutes;
        if (minutes && minutes % 1440 === 0)
            return (minutes / 1440) + "D";
        if (minutes && minutes % 60 === 0)
            return (minutes / 60) + "H";
        if (minutes)
            return minutes + "M";
        return window.label || window.id || "Quota";
    }

    function overviewWindow(window: var): string {
        if (!window)
            return "";
        const context = window.model || window.bucket_id || "";
        const name = shortWindow(window);
        return context && context !== name ? name + " · " + context : name;
    }

    function detailWindow(window: var, windows: var): string {
        const name = windowName(window);
        if (windows.filter(w => windowName(w) === name).length < 2)
            return name;
        const peers = windows.filter(w => windowName(w) === name);
        const candidates = [windowDetail(window), window.bucket_id, window.id];
        const context = candidates.find(value => value && peers.filter(w => [windowDetail(w), w.bucket_id, w.id].includes(value)).length === 1) || window.id || windowDetail(window) || window.bucket_id || "";
        return context && context !== name ? name + " · " + context : name;
    }

    function displayTime(value: var): string {
        if (!value)
            return "Reset unknown";
        const date = new Date(value);
        if (isNaN(date.getTime()))
            return "Reset unknown";
        if (date.getTime() <= Date.now())
            return "Reset passed";
        return Qt.formatDateTime(date, "dd MMM, HH:mm");
    }

    function timeUntil(value: var): string {
        if (!value)
            return "";
        const remaining = new Date(value).getTime() - Date.now();
        if (!isFinite(remaining) || remaining <= 0)
            return "";
        const minutes = Math.ceil(remaining / 60000);
        const days = Math.floor(minutes / 1440);
        const hours = Math.floor((minutes % 1440) / 60);
        const mins = minutes % 60;
        if (days > 0)
            return "in " + days + "d" + (hours > 0 ? " " + hours + "h" : "");
        if (hours > 0)
            return "in " + hours + "h" + (mins > 0 ? " " + mins + "m" : "");
        return "in " + mins + "m";
    }

    function statusText(status: string): string {
        switch (status) {
        case "healthy": return "";
        case "stale": return "Stale";
        case "failed": return "Update failed";
        case "unavailable": return "Unavailable";
        case "unauthenticated": return "Sign in required";
        case "unsupported": return "Unsupported";
        case "refreshing": return "Updating";
        case "disabled": return "Disabled";
        default: return status || "Unknown";
        }
    }

    implicitWidth: Math.max(0, Math.min(480, maximumWidth))
    implicitHeight: Math.min(contentColumn.implicitHeight, 660)
    contentWidth: width
    contentHeight: contentColumn.implicitHeight
    clip: true
    boundsBehavior: Flickable.StopAtBounds

    TapHandler {
        acceptedButtons: Qt.LeftButton
        gesturePolicy: TapHandler.DragThreshold
        onPressedChanged: {
            if (pressed)
                root.interactionRequested();
        }
    }

    Component.onCompleted: Qt.callLater(readCache)

    Timer {
        interval: 30000
        running: true
        repeat: true
        onTriggered: root.readCache()
    }

    Timer {
        id: readerTimeout
        interval: 5000
        onTriggered: {
            if (!root.readerStarted && !reader.running) {
                // Failed starts have no exited signal in Quickshell.
                root.cacheUnavailable();
                root.finishCacheRead();
                return;
            }
            root.readerTimedOut = true;
            root.providers = [];
            root.closeProvider();
            root.error = "Reading AIQuota cached status timed out.";
            reader.running = false;
            readerKillTimeout.restart();
        }
    }

    Timer {
        id: readerKillTimeout
        interval: 1000
        // Process.signal targets the tracked child, avoiding a PID reuse race.
        onTriggered: if (root.readingCache && root.readerTimedOut) reader.signal(9)
    }

    Process {
        id: reader
        onStarted: root.readerStarted = true
        stdout: StdioCollector {
            onStreamFinished: {
                if (!root.readingCache)
                    return;
                root.readerOutput = text;
                root.readerStreamFinished = true;
                root.completeCacheRead();
            }
        }
        onExited: (exitCode, exitStatus) => {
            if (!root.readingCache)
                return;
            root.readerExited = true;
            root.readerSucceeded = exitCode === 0 && exitStatus === 0;
            root.completeCacheRead();
        }
    }

    Column {
        id: contentColumn
        width: root.width
        spacing: 0

        Column {
            width: parent.width
            spacing: 8
            topPadding: 12
            bottomPadding: 12
            leftPadding: 12
            rightPadding: 12

            RowLayout {
                width: parent.width - parent.leftPadding - parent.rightPadding
                spacing: 10

                Button {
                    id: backButton
                    objectName: "back-to-providers"
                    visible: root.selectedProviderId !== ""
                    text: "‹"
                    Accessible.name: "Back to providers"
                    ToolTip.visible: hovered || activeFocus
                    ToolTip.delay: 400
                    ToolTip.text: Accessible.name
                    font.pixelSize: 22
                    implicitWidth: 28
                    implicitHeight: 28
                    background: Rectangle { radius: 10; color: root.cardColor; border.color: backButton.activeFocus || backButton.hovered || backButton.pressed ? root.textColor : root.borderColor }
                    contentItem: Text { text: backButton.text; color: root.textColor; font: backButton.font; horizontalAlignment: Text.AlignHCenter; verticalAlignment: Text.AlignVCenter }
                    onPressed: root.interactionRequested()
                    onClicked: root.closeProvider()
                }

                ColumnLayout {
                    Layout.fillWidth: true
                    Layout.minimumWidth: 0
                    spacing: 2
                    StyledText {
                        visible: root.selectedProvider === null
                        Layout.fillWidth: true
                        text: "AI Quota"
                        color: Colours.palette.m3onSurface
                        font.pixelSize: 17
                        font.bold: true
                        elide: Text.ElideRight
                    }
                }

                Flickable {
                    id: providerSwitcher
                    objectName: "provider-switcher"
                    visible: root.selectedProvider !== null
                    Layout.preferredWidth: Math.min(switcherRow.width + 8, Math.max(0, parent.width - 48))
                    Layout.minimumWidth: 0
                    Layout.preferredHeight: 36
                    contentWidth: switcherRow.width + 8
                    contentHeight: height
                    clip: true
                    boundsBehavior: Flickable.StopAtBounds
                    flickableDirection: Flickable.HorizontalFlick

                    function reveal(item: Item): void {
                        contentX = Math.max(0, Math.min(contentWidth - width, item.x + item.width / 2 + 4 - width / 2));
                    }
                    function revealSelected(): void {
                        Qt.callLater(() => {
                            if (!visible) return;
                            for (let i = 0; i < switcherRepeater.count; i++) {
                                const item = switcherRepeater.itemAt(i);
                                if (item && item.activeFocus) {
                                    reveal(item);
                                    return;
                                }
                            }
                            for (let i = 0; i < switcherRepeater.count; i++) {
                                const item = switcherRepeater.itemAt(i);
                                if (item && item.modelData.id === root.selectedProviderId) {
                                    reveal(item);
                                    return;
                                }
                            }
                        });
                    }
                    function updateModel(): void {
                        let focusedId = "";
                        for (let i = 0; i < switcherRepeater.count; i++) {
                            const item = switcherRepeater.itemAt(i);
                            if (item && item.activeFocus) {
                                focusedId = item.modelData.id;
                                break;
                            }
                        }
                        switcherRepeater.model = root.orderedProviders(root.providers);
                        if (focusedId) {
                            for (let i = 0; i < switcherRepeater.count; i++) {
                                const item = switcherRepeater.itemAt(i);
                                if (item && item.modelData.id === focusedId) {
                                    item.forceActiveFocus(Qt.OtherFocusReason);
                                    break;
                                }
                            }
                        }
                        revealSelected();
                    }
                    onVisibleChanged: if (visible) revealSelected()
                    onWidthChanged: revealSelected()
                    Component.onCompleted: updateModel()
                    Connections {
                        target: root
                        function onSelectedProviderIdChanged(): void { providerSwitcher.revealSelected(); }
                        function onProvidersChanged(): void { providerSwitcher.updateModel(); }
                    }

                    Rectangle {
                        width: providerSwitcher.contentWidth
                        height: parent.height
                        radius: height / 2
                        color: Colours.tPalette.m3surfaceContainer
                    }
                    Rectangle {
                        objectName: "switcher-indicator"
                        width: 32; height: 28; radius: 14
                        anchors.verticalCenter: parent.verticalCenter
                        visible: root.selectedProvider !== null
                        x: 4 + Math.max(0, root.orderedProviders(root.providers).findIndex(p => p.id === root.selectedProviderId)) * 32
                        color: Colours.palette.m3primary
                        Behavior on x { Anim { type: Anim.DefaultSpatial } }
                    }
                    Row {
                        id: switcherRow
                        x: 4
                        anchors.verticalCenter: parent.verticalCenter
                        onPositioningComplete: providerSwitcher.revealSelected()
                        Repeater {
                            id: switcherRepeater
                            onCountChanged: providerSwitcher.revealSelected()
                            delegate: Button {
                                id: switchButton
                                required property var modelData
                                objectName: "switch-provider-" + modelData.id
                                width: 32; height: 28
                                Accessible.name: (modelData.name || modelData.id || "Provider") + (root.selectedProviderId === modelData.id ? ", selected" : ", show insights")
                                ToolTip.visible: hovered || activeFocus
                                ToolTip.delay: 400
                                ToolTip.text: modelData.name || modelData.id || "Provider"
                                onActiveFocusChanged: if (activeFocus) providerSwitcher.reveal(switchButton)
                                onPressed: root.interactionRequested()
                                onClicked: {
                                    root.selectedProviderId = modelData.id;
                                    root.contentY = 0;
                                    providerSwitcher.reveal(switchButton);
                                }
                                background: Rectangle {
                                    radius: height / 2
                                    color: switchButton.pressed ? Qt.rgba(1, 1, 1, 0.18) : switchButton.hovered ? Qt.rgba(1, 1, 1, 0.1) : "transparent"
                                    border.color: switchButton.activeFocus ? Colours.palette.m3onSurface : "transparent"
                                }
                                contentItem: Item {
                                    Image {
                                        anchors.centerIn: parent
                                        width: 16; height: 16
                                        sourceSize.width: 42; sourceSize.height: 42
                                        source: ["claude", "codex", "antigravity"].includes(switchButton.modelData.id) ? Qt.resolvedUrl("aiquota-icons/" + switchButton.modelData.id + ".svg") : ""
                                        fillMode: Image.PreserveAspectFit
                                        layer.enabled: true
                                        layer.effect: MultiEffect { colorization: 1; colorizationColor: root.selectedProviderId === switchButton.modelData.id ? Colours.palette.m3onPrimary : Colours.palette.m3onSurface }
                                    }
                                    StyledText {
                                        anchors.centerIn: parent
                                        visible: !["claude", "codex", "antigravity"].includes(switchButton.modelData.id)
                                        text: (switchButton.modelData.id || "?").slice(0, 2).toUpperCase()
                                        color: root.selectedProviderId === switchButton.modelData.id ? Colours.palette.m3onPrimary : Colours.palette.m3onSurface
                                        font: Tokens.font.body.small
                                    }
                                }
                            }
                        }
                    }
                }
            }

            StyledText {
                visible: root.error.length > 0
                width: parent.width - parent.leftPadding - parent.rightPadding
                elide: Text.ElideRight
                text: "Status unavailable"
                color: Colours.palette.m3onSurfaceVariant
                ToolTip.visible: errorHover.hovered
                ToolTip.text: root.error
                HoverHandler { id: errorHover }
            }
            Grid {
                id: providerGrid
                objectName: "provider-overview"
                visible: root.selectedProviderId === ""
                width: parent.width - parent.leftPadding - parent.rightPadding
                columns: Math.max(1, Math.min(3, Math.floor((width + spacing) / 110)))
                spacing: 8
                readonly property real cardHeight: {
                    let height = 0;
                    for (const item of children) {
                        if (item.implicitHeight > height)
                            height = item.implicitHeight;
                    }
                    return height;
                }

                Repeater {
                    model: root.orderedProviders(root.providers)
                    delegate: Rectangle {
                        id: card
                        required property var modelData
                        objectName: "provider-card-" + modelData.id
                        function focusCard(): void { cardButton.forceActiveFocus(Qt.OtherFocusReason); }
                        readonly property var primary: root.primaryWindow(modelData.windows)
                        readonly property var palette: root.providerPalette(modelData.id || "")
                        width: Math.max(0, (providerGrid.width - (providerGrid.columns - 1) * providerGrid.spacing) / providerGrid.columns)
                        implicitHeight: cardContent.implicitHeight + 23
                        height: providerGrid.cardHeight
                        radius: 16
                        color: card.palette.card
                        border.color: cardButton.activeFocus || cardButton.hovered || cardButton.pressed ? card.palette.text : card.palette.accent

                        Button {
                            id: cardButton
                            objectName: "card-" + card.modelData.id
                            anchors.fill: parent
                            z: 0
                            background: Item {}
                            Accessible.name: (card.modelData.name || card.modelData.id) + ", " + root.providerStatus(card.modelData) + ", " + root.windowIdentity(card.primary) + ", " + root.quotaDescription(card.primary ? card.primary.remaining_percent : null) + ". Open insights"
                            ToolTip.visible: activeFocus
                            ToolTip.text: root.windowIdentity(card.primary) + (card.modelData.message ? " · " + card.modelData.message : "")
                            onPressed: root.interactionRequested()
                            onClicked: root.openProvider(card.modelData.id)
                        }

                        Column {
                            id: cardContent
                            x: 10; y: 10
                            width: Math.max(0, parent.width - 23)
                            spacing: 5

                            RowLayout {
                                width: parent.width
                                spacing: 5
                                Rectangle {
                                    id: providerIcon
                                    objectName: "provider-" + card.modelData.id
                                    property string label: card.modelData.name || card.modelData.id || "Provider"
                                    Layout.preferredWidth: 24
                                    Layout.preferredHeight: 24
                                    radius: 7
                                    color: card.palette.card
                                    border.color: card.palette.accent
                                    Accessible.role: Accessible.Graphic
                                    Accessible.name: label
                                    ToolTip.visible: iconHover.hovered
                                    ToolTip.delay: 400
                                    ToolTip.text: label
                                    HoverHandler { id: iconHover }
                                    Image {
                                        objectName: "logo-" + card.modelData.id
                                        anchors.centerIn: parent
                                        width: 15; height: 15
                                        sourceSize.width: 42; sourceSize.height: 42
                                        fillMode: Image.PreserveAspectFit
                                        source: ["claude", "codex", "antigravity"].includes(card.modelData.id) ? Qt.resolvedUrl("aiquota-icons/" + card.modelData.id + ".svg") : ""
                                        layer.enabled: true
                                        layer.effect: MultiEffect { colorization: 1; colorizationColor: card.palette.text }
                                    }
                                    StyledText {
                                        anchors.centerIn: parent
                                        visible: !["claude", "codex", "antigravity"].includes(card.modelData.id)
                                        text: (card.modelData.id || "?").slice(0, 2).toUpperCase()
                                        color: card.palette.text
                                        font: Tokens.font.body.small
                                    }
                                }
                                StyledText {
                                    Layout.fillWidth: true
                                    Layout.minimumWidth: 0
                                    text: card.modelData.name || card.modelData.id || "Provider"
                                    color: card.palette.text
                                    font.pixelSize: 12
                                    font.bold: true
                                    elide: Text.ElideRight
                                }
                            }
                            Row {
                                width: parent.width
                                spacing: 5
                                StyledText {
                                    text: card.primary ? Math.round(card.primary.remaining_percent) + "%" : "N/A"
                                    color: card.palette.text
                                    font.pixelSize: 25
                                    font.bold: true
                                }
                            }
                            StyledText {
                                width: parent.width
                                visible: card.primary !== null
                                text: root.overviewWindow(card.primary)
                                color: card.palette.text
                                font: Tokens.font.body.small
                                elide: Text.ElideRight
                                ToolTip.visible: overviewLabelHover.hovered
                                ToolTip.text: root.windowIdentity(card.primary)
                                HoverHandler { id: overviewLabelHover }
                            }
                            Rectangle {
                                width: parent.width; height: 6; radius: 3
                                color: card.palette.track || card.palette.border
                                visible: card.primary !== null
                                Rectangle {
                                    objectName: "overview-progress-" + card.modelData.id
                                    width: parent.width * Math.max(0, Math.min(100, card.primary ? card.primary.remaining_percent : 0)) / 100
                                    height: parent.height; radius: parent.radius
                                    color: root.quotaColor(card.primary ? card.primary.remaining_percent : null)
                                }
                            }
                            StyledText {
                                width: parent.width
                                text: card.primary ? (root.timeUntil(card.primary.resets_at) ? "Reset " + root.timeUntil(card.primary.resets_at) : root.displayTime(card.primary.resets_at)) : "Reset unknown"
                                color: card.palette.text
                                font: Tokens.font.body.small
                                elide: Text.ElideRight
                            }
                            StyledText {
                                width: parent.width
                                visible: card.modelData.status !== "healthy"
                                text: root.statusText(card.modelData.status || "")
                                color: card.palette.text
                                font: Tokens.font.body.small
                                elide: Text.ElideRight
                                ToolTip.visible: statusHover.hovered && !!card.modelData.message
                                ToolTip.text: card.modelData.message || ""
                                HoverHandler { id: statusHover }
                            }
                        }
                    }
                }
            }

            Column {
                id: details
                objectName: "provider-insights"
                visible: root.selectedProvider !== null
                width: parent.width - parent.leftPadding - parent.rightPadding
                spacing: 8
                readonly property var provider: root.selectedProvider || ({})
                readonly property var palette: root.providerPalette(provider.id || "")

                RowLayout {
                    width: parent.width
                    spacing: 8
                    visible: details.provider.status !== "healthy" || !!root.accountSummary(details.provider)
                    StyledText {
                        Layout.fillWidth: true
                        visible: details.provider.status !== "healthy"
                        activeFocusOnTab: !!details.provider.message
                        text: root.providerStatus(details.provider)
                        color: Colours.palette.m3onSurface
                        font.pixelSize: 13
                        font.bold: true
                        elide: Text.ElideRight
                        ToolTip.visible: (detailStatusHover.hovered || activeFocus) && !!details.provider.message
                        ToolTip.text: details.provider.message || ""
                        HoverHandler { id: detailStatusHover }
                    }
                    StyledText {
                        text: root.accountSummary(details.provider)
                        color: Colours.palette.m3onSurfaceVariant
                        font: Tokens.font.body.small
                        wrapMode: Text.Wrap
                        Layout.maximumWidth: parent.width * 0.65
                    }

                }
                StyledText {
                    visible: !(details.provider.windows || []).length
                    width: parent.width
                    text: "No quota windows available."
                    color: Colours.palette.m3onSurfaceVariant
                    font: Tokens.font.body.small
                    wrapMode: Text.Wrap
                }
                Repeater {
                    model: root.groups(details.provider.windows)
                    delegate: Column {
                        id: group
                        required property var modelData
                        width: details.width
                        spacing: 8
                        StyledText {
                            visible: !!group.modelData.name
                            width: parent.width
                            text: group.modelData.name || ""
                            color: Colours.palette.m3onSurface
                            font.pixelSize: 13
                            font.bold: true
                            wrapMode: Text.Wrap
                        }
                        Grid {
                            id: windowGrid
                            width: parent.width
                            columns: width >= 340 ? 2 : 1
                            spacing: 8
                            Repeater {
                                model: group.modelData.windows
                                delegate: Rectangle {
                                    id: tile
                                    objectName: "detail-tile-" + (modelData.id || "")
                                    required property var modelData
                                    width: Math.max(0, (windowGrid.width - (windowGrid.columns - 1) * windowGrid.spacing) / windowGrid.columns)
                                    implicitHeight: tileContent.implicitHeight + 20
                                    height: implicitHeight
                                    radius: 12
                                    color: details.palette.card
                                    border.color: details.palette.border
                                    Accessible.role: Accessible.StaticText
                                    Accessible.name: (details.provider.name || details.provider.id || "Provider") + ", " + root.providerStatus(details.provider) + ", " + (group.modelData.name || "") + ", " + root.windowIdentity(modelData) + ", " + root.quotaDescription(modelData.remaining_percent) + ", " + root.displayTime(modelData.resets_at) + ", " + root.amountText(modelData)
                                    activeFocusOnTab: true
                                    ToolTip.visible: tileHover.hovered || activeFocus
                                    ToolTip.text: root.windowIdentity(tile.modelData) + (root.amountText(tile.modelData) ? " · " + root.amountText(tile.modelData) : "") + " · " + root.displayTime(tile.modelData.resets_at)
                                    HoverHandler { id: tileHover }
                                    Column {
                                        id: tileContent
                                        x: 10; y: 10
                                        width: Math.max(0, parent.width - 20)
                                        spacing: 5
                                        RowLayout {
                                            width: parent.width
                                            spacing: 8
                                            StyledText {
                                                Layout.fillWidth: true
                                                Layout.minimumWidth: 0
                                                text: root.detailWindow(tile.modelData, group.modelData.windows)
                                                color: details.palette.text
                                                font.pixelSize: 13
                                                font.bold: true
                                                wrapMode: Text.Wrap
                                            }
                                            StyledText {
                                                Layout.maximumWidth: parent.width * 0.5
                                                text: root.quotaState(tile.modelData.remaining_percent) === "unknown" ? (root.compactAmount(tile.modelData) || "N/A") : Math.round(tile.modelData.remaining_percent) + "%"
                                                color: details.palette.text
                                                font.pixelSize: 15
                                                font.bold: true
                                                elide: Text.ElideRight
                                            }
                                        }
                                        Rectangle {
                                            width: parent.width; height: 6; radius: 3
                                            color: details.palette.track || details.palette.border
                                            visible: root.quotaState(tile.modelData.remaining_percent) !== "unknown"
                                            Rectangle {
                                                objectName: "detail-progress-" + (tile.modelData.id || "")
                                                width: parent.width * (root.quotaState(tile.modelData.remaining_percent) === "unknown" ? 0 : Math.max(0, Math.min(100, tile.modelData.remaining_percent))) / 100
                                                height: parent.height; radius: parent.radius
                                                color: root.quotaColor(tile.modelData.remaining_percent)
                                            }
                                        }
                                        RowLayout {
                                            width: parent.width
                                            spacing: 8
                                            StyledText {
                                                Layout.fillWidth: true
                                                Layout.minimumWidth: 0
                                                text: root.timeUntil(tile.modelData.resets_at) ? "Reset " + root.timeUntil(tile.modelData.resets_at) : root.displayTime(tile.modelData.resets_at)
                                                color: details.palette.text
                                                font: Tokens.font.body.small
                                                elide: Text.ElideRight
                                            }
                                            StyledText {
                                                visible: root.quotaState(tile.modelData.remaining_percent) !== "unknown" && !!root.compactAmount(tile.modelData)
                                                Layout.maximumWidth: parent.width * 0.5
                                                text: root.compactAmount(tile.modelData)
                                                color: details.palette.text
                                                font: Tokens.font.body.small
                                                elide: Text.ElideRight
                                            }
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}
