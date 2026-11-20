package io.github.sudiptadeb.amarit

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class UpdateNotificationTest {
    private fun shown(json: String) = UpdateNotification.shown(JSONObject(json))

    @Test
    fun downloadingShowsProgressWithCancel() {
        val s = shown("""{"phase":"downloading","version":"0.3.9","done":50,"total":200}""")!!
        assertEquals(R.string.amarit_downloading_app, s.title)
        assertEquals(50L, s.done)
        assertEquals(200L, s.total)
        assertEquals(true, s.ongoing)
        assertEquals("cancel", s.command)
    }

    @Test
    fun verifyingIsABarWithoutAnEnd() {
        val s = shown("""{"phase":"verifying","version":"0.3.9"}""")!!
        assertEquals(true, s.indeterminate)
        assertEquals("cancel", s.command)
    }

    @Test
    fun readyOffersInstall() {
        val s = shown("""{"phase":"ready","version":"0.3.9"}""")!!
        assertEquals("relaunch", s.command)
        assertEquals(R.string.amarit_install, s.commandLabel)
        assertEquals(false, s.ongoing)
    }

    @Test
    fun onlyAFailureRetryCanHelpIsShown() {
        for (kind in listOf("network", "stalled", "damaged", "unchecked", "install")) {
            assertEquals(kind, "retry", shown("""{"phase":"failed","version":"0.3.9","kind":"$kind"}""")?.command)
        }
        for (kind in listOf("refused", "not-writable", "busy", "offline")) {
            assertNull(kind, shown("""{"phase":"failed","kind":"$kind"}"""))
        }
    }

    @Test
    fun theRestTakeItAway() {
        for (phase in listOf("", "checking", "current")) {
            assertNull(phase, shown("""{"phase":"$phase"}"""))
        }
    }
}
