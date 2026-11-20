#ifndef MACAPP_PRESENT_DARWIN_H
#define MACAPP_PRESENT_DARWIN_H

void macapp_dock_progress(double fraction);
void macapp_dock_clear(void);
void macapp_notify(const char* title, const char* body, const char* button);
void macapp_notify_remove(void);
char* macapp_app_name(void);

#endif
