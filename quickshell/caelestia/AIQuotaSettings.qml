pragma ComponentBehavior: Bound

import QtQuick
import QtQuick.Layouts
import QtCore
import Quickshell
import Quickshell.Io
import Caelestia.Config
import Caelestia.I18n
import qs.components
import qs.components.controls
import qs.modules.nexus.common

PageBase {
    id: root

    property var providers: []
    property string error: ""
    property bool busy: false
    readonly property string executable: {
        const home = Quickshell.env("HOME");
        const local = String(StandardPaths.findExecutable("aiquota", home ? [home + "/.local/bin"] : []));
        return local.startsWith("file://") ? decodeURIComponent(local.slice(7)) : "aiquota";
    }

    title: Tr.tr("AIQuota")

    function loadStatus(): void {
        if (reader.running || busy)
            return;
        reader.command = [executable, "status", "--cached", "--json"];
        reader.running = true;
    }

    function setProvider(id: string, enabled: bool): void {
        if (busy)
            return;
        busy = true;
        error = "";
        toggle.command = [executable, "provider", enabled ? "enable" : "disable", id];
        toggle.running = true;
    }

    ColumnLayout {
        anchors.horizontalCenter: parent.horizontalCenter
        anchors.top: parent.top
        width: root.cappedWidth
        spacing: Tokens.spacing.extraSmall / 2

        Process {
            id: reader
            stdout: StdioCollector {
                onStreamFinished: {
                    try {
                        const data = JSON.parse(text);
                        if (data.schema_version !== 1 || !Array.isArray(data.providers))
                            throw new Error("Unsupported status schema");
                        root.providers = data.providers;
                        root.error = "";
                    } catch (e) {
                        root.error = Tr.tr("Could not read AIQuota status.");
                    }
                }
            }
            onExited: (code, status) => {
                if (code !== 0 || status !== 0)
                    root.error = Tr.tr("AIQuota is unavailable. Check ~/.local/bin/aiquota.");
            }
        }

        Process {
            id: toggle
            onExited: (code, status) => {
                root.busy = false;
                if (code !== 0 || status !== 0)
                    root.error = Tr.tr("Could not change provider setting.");
                root.loadStatus();
            }
        }

        Timer {
            running: root.visible
            repeat: true
            triggeredOnStart: true
            interval: 5000
            onTriggered: root.loadStatus()
        }

        StyledText {
            Layout.fillWidth: true
            Layout.leftMargin: Tokens.padding.small
            text: Tr.tr("Coding tool quota status")
            color: Colours.palette.m3onSurfaceVariant
            font: Tokens.font.label.medium
        }

        Repeater {
            model: root.providers

            ToggleRow {
                required property var modelData
                required property int index

                first: index === 0
                last: index === root.providers.length - 1
                text: modelData.name
                subtext: modelData.message || modelData.status
                checked: modelData.enabled
                disabled: root.busy
                onToggled: root.setProvider(modelData.id, checked)
            }
        }

        StyledText {
            Layout.fillWidth: true
            Layout.leftMargin: Tokens.padding.small
            visible: root.providers.length === 0 && !root.error
            text: Tr.tr("Waiting for AIQuota status…")
            color: Colours.palette.m3outline
            font: Tokens.font.body.small
        }

        StyledText {
            Layout.fillWidth: true
            Layout.leftMargin: Tokens.padding.small
            visible: !!root.error
            text: root.error
            color: Colours.palette.m3error
            font: Tokens.font.body.small
            wrapMode: Text.WordWrap
        }

        SectionHeader {
            text: Tr.tr("Collection")
        }

        NavRow {
            first: true
            last: true
            icon: "refresh"
            text: Tr.tr("Refresh status")
            subtext: Tr.tr("Read the latest cached quota data")
            disabled: root.busy
            onClicked: root.loadStatus()
        }
    }
}
