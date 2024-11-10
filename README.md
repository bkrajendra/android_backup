# Android Backup and resume shell script

Copy and photos from android mobile to PC with option to resume if download breaks in between.

## Usage

### Install ADB (if not already installed):

- On Windows: Download the Android SDK Platform Tools and extract the ZIP file.
- On macOS and Linux: Use a package manager (brew install android-platform-tools for macOS) or download and extract the Platform Tools ZIP file from the Android developer site.
- https://developer.android.com/tools/releases/platform-tools

### Enable Developer Options on your Android device:

- Go to Settings > About phone.
- Tap Build number multiple times until it says "You are now a developer!"
- Go back to Settings > System > Developer options and enable USB Debugging.

### Connect the Device via USB:

- Use a USB cable to connect your device to the computer.

### Authorize the Device:

- When you plug the device into the computer, your Android device may prompt you to Allow USB debugging. Tap Allow.


Run the ADB Shell 
```
adb devices
```
Output:
```
List of devices attached
RZXXXXXXXXX     device
```
```
adb shell
```
This should open the interactive shell.
If all above stuff works, you are ready to start.

Run the shell script as follows:

- On windows open the git bash or MobaXterm (Windows paths might be different on both terminals)
```
./backup.sh [-r remote_folder] [-l local_folder]
e.g. ./backup.sh sdcard/DCIM/Camera /f/Photo/A52