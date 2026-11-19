import QtQuick
import Quickshell
ShellRoot {
    Window {
        id: win
        title: "AIQuota Hover Test"
        visible: true
        width: 400; height: 300
        property alias panel: dummy
        Item { id: dummy; visible: false; property int offsetScale: 1; property bool hovered: false }
        property var state: QtObject { property bool bar: false; property bool osd: false; property bool sidebar: false; property bool session: false; property bool dashboard: false; property bool launcher: false; property bool utilities: false }
        property var pops: ({ currentName: "", currentCenter: 0, hasCurrent: false, isDetached: false })
        property var bars: ({ position: "top", implicitWidth: 400, implicitHeight: 40, clampedWidth: 400, clampedHeight: 40, isHovered: false, closeTray: function(){}, checkPopout: function(x) {}, hasNonTitleEntryAt: function(x){return false} })
        property var allPanels: ({ popoutsRect: Qt.rect(80,60,320,180), dashboardHitRect: Qt.rect(0,0,0,0), osd: win.panel, osdWrapper: win.panel, session: win.panel, sessionWrapper: win.panel, sidebar: win.panel, utilities: win.panel, launcher: win.panel, dashboard: win.panel, notifications: win.panel, exposedRect: function(x){return Qt.rect(0,0,0,0)}, panelGeometry: function(x){return Qt.rect(0,0,0,0)} })
        Interactions { id: area; screen: Quickshell.screens[0]; popouts: win.pops; screenState: win.state; panels: win.allPanels; bar: win.bars; borderThickness: 2; fullscreen: false }
        Timer { interval: 1500; running: true; onTriggered: console.log("EARLY_STATE", win.pops.hasCurrent) }
        Timer { interval: 8000; running: true; onTriggered: { console.log("FINAL_STATE", win.pops.hasCurrent); Qt.quit(); } }
    }
}
