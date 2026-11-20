package io.github.sudiptadeb.amarit

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import org.json.JSONObject

/**
 * The system's notification for an app updater that runs elsewhere, such as
 * the Go updater amarit/appupdate: the app hands it each State, as the JSON
 * the updater makes, and gets the person's taps back as commands.
 *
 *     UpdateNotification.commands = UpdateNotification.Commands { name -> updater.send(name) }
 *     updater.onChange { json -> UpdateNotification.show(context, json) }
 *
 * Downloading shows the progress with Cancel; verifying, an indeterminate
 * bar; ready, the version with Install; a download that failed in a way a
 * retry can help, Try again. Every other state takes the notification away.
 * The words are string resources (amarit_*), which an app overrides with its
 * own. The app asks for POST_NOTIFICATIONS on Android 13 and later.
 */
object UpdateNotification {
    /** The person's taps: "cancel", "retry" or "relaunch". */
    fun interface Commands {
        fun command(name: String)
    }

    /** Where taps go. A tap while the app's process is gone opens the app. */
    @Volatile
    var commands: Commands? = null

    const val CHANNEL = "amarit_progress"
    const val ID = 0x414E

    /** What the notification says for one state; null takes it away. */
    data class Shown(
        val title: Int,
        val version: String,
        val text: Int = 0,
        /** Bytes, when known; total 0 with [indeterminate] is a bar without an end. */
        val done: Long = 0,
        val total: Long = 0,
        val indeterminate: Boolean = false,
        val ongoing: Boolean = false,
        val command: String? = null,
        val commandLabel: Int = 0,
    )

    /** The table in docs/appupdate.md, for a notification. */
    fun shown(state: JSONObject): Shown? {
        val version = state.optString("version")
        return when (state.optString("phase")) {
            "downloading" -> Shown(
                R.string.amarit_downloading_app, version,
                done = state.optLong("done"), total = state.optLong("total"),
                indeterminate = state.optLong("total") <= 0,
                ongoing = true, command = "cancel", commandLabel = R.string.amarit_cancel,
            )
            "verifying" -> Shown(
                R.string.amarit_verifying, version, indeterminate = true,
                ongoing = true, command = "cancel", commandLabel = R.string.amarit_cancel,
            )
            "ready" -> Shown(
                R.string.amarit_ready, version, text = R.string.amarit_ready_text,
                command = "relaunch", commandLabel = R.string.amarit_install,
            )
            "failed" -> when (state.optString("kind")) {
                "network", "stalled", "damaged", "unchecked", "install" -> Shown(
                    R.string.amarit_failed, version, text = R.string.amarit_failed_text,
                    command = "retry", commandLabel = R.string.amarit_retry,
                )
                else -> null
            }
            else -> null
        }
    }

    /** Shows [stateJson], appupdate's State, as the notification. */
    fun show(context: Context, stateJson: String) {
        val nm = context.getSystemService(NotificationManager::class.java) ?: return
        val s = shown(JSONObject(stateJson))
        if (s == null) {
            nm.cancel(ID)
            return
        }
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL, context.getString(R.string.amarit_channel_name), NotificationManager.IMPORTANCE_LOW),
        )
        val label = context.applicationInfo.loadLabel(context.packageManager).toString()
        val b = Notification.Builder(context, CHANNEL)
            .setSmallIcon(if (s.ongoing) android.R.drawable.stat_sys_download else android.R.drawable.stat_sys_download_done)
            .setContentTitle(context.getString(s.title, label, s.version))
            .setOngoing(s.ongoing)
            .setOnlyAlertOnce(true)
            .setAutoCancel(!s.ongoing)
        context.packageManager.getLaunchIntentForPackage(context.packageName)?.let {
            b.setContentIntent(PendingIntent.getActivity(context, 0, it, PendingIntent.FLAG_IMMUTABLE))
        }
        if (s.text != 0) b.setContentText(context.getString(s.text, label, s.version))
        if (s.ongoing) {
            val max = if (s.total > 0) 1000 else 0
            val at = if (s.total > 0) (s.done * 1000 / s.total).toInt() else 0
            b.setProgress(max, at, s.indeterminate)
        }
        s.command?.let { cmd ->
            val tap = Intent(context, UpdateCommandReceiver::class.java).putExtra(UpdateCommandReceiver.COMMAND, cmd)
            val pi = PendingIntent.getBroadcast(context, cmd.hashCode(), tap, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
            b.addAction(Notification.Action.Builder(null, context.getString(s.commandLabel), pi).build())
        }
        nm.notify(ID, b.build())
    }
}

/** Takes a notification's tap to [UpdateNotification.commands], or opens the app when it has none. */
class UpdateCommandReceiver : BroadcastReceiver() {
    companion object {
        const val COMMAND = "io.github.sudiptadeb.amarit.COMMAND"
    }

    override fun onReceive(context: Context, intent: Intent) {
        val cmd = intent.getStringExtra(COMMAND) ?: return
        val to = UpdateNotification.commands
        if (to != null) {
            to.command(cmd)
            return
        }
        context.packageManager.getLaunchIntentForPackage(context.packageName)?.let {
            it.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            context.startActivity(it)
        }
    }
}
