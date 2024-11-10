#!/bin/bash

# Default remote and local folders
REMOTE_FOLDER="sdcard/DCIM/Camera"
LOCAL_FOLDER="$HOME/Downloads/CameraBackup"

# Usage
usage() {
  echo "Usage: $0 [-r remote_folder] [-l local_folder]"
  echo "  -r: Set the remote folder path on the Android device (default: $REMOTE_FOLDER)"
  echo "  -l: Set the local folder path to save files (default: $LOCAL_FOLDER)"
  exit 1
}

# Parse command-line arguments
while getopts "r:l:h" opt; do
  case $opt in
    r) REMOTE_FOLDER="$OPTARG" ;;
    l) LOCAL_FOLDER="$OPTARG" ;;
    h) usage ;;
    *) usage ;;
  esac
done

# Check dependencies
check_dependencies() {
  if ! command -v adb &>/dev/null; then
    echo "Error: adb is not installed. Install it first."
    exit 1
  else
    echo "ADB cli tool found."
    adb devices | grep -q "device" || { echo "Error: No Android device found. Connect android device with USB and enable USB debugging."; exit 1; }
  fi
}

# List files on Android device
list_android_files() {
  adb shell ls "$REMOTE_FOLDER" > $LOCAL_FOLDER/android.files
  FILE_COUNT=$(wc -l < $LOCAL_FOLDER/android.files)  # Get total file count
}

# List local files
list_local_files() {
  ls -1 "$LOCAL_FOLDER" > $LOCAL_FOLDER/local.files
}

# Generate update list
generate_update_list() {
  echo "Preparing update list. This might take few minutes based on number files in the CAMERA folder..."
  rm -f $LOCAL_FOLDER/update.files
  touch $LOCAL_FOLDER/update.files

  while IFS= read -r line; do
    # Remove non-printable characters
    clean_line=$(echo "$line" | sed 's/[^[:print:]]//')
    # If file doesn't exist locally, add to update list
    if ! grep -q "$clean_line" $LOCAL_FOLDER/local.files; then
      echo "$clean_line" >> $LOCAL_FOLDER/update.files
    fi
  done < $LOCAL_FOLDER/android.files
  echo "Update list prepared!"
}

# Download files by checking missing files
download_files() {
  echo "Starting File download..."
  PROGRESS=0
  while IFS= read -r line; do
    clean_line=$(echo "$line" | sed 's/[^[:print:]]//')
    echo "Downloading ($((++PROGRESS))/$FILE_COUNT): $clean_line"
    adb pull "$REMOTE_FOLDER/$clean_line" "$LOCAL_FOLDER/$clean_line"
  done < "$LOCAL_FOLDER/update.files"
  echo "Download complete!"
}

# Entry point
main() {
  check_dependencies
  list_android_files
  list_local_files
  generate_update_list
  download_files
}

# Start the magic
main
