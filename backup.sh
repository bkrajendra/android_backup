#!/bin/sh

rfolder=sdcard/DCIM/Camera
lfolder=/f/sarika/nord2backup/Camera

adb shell ls "$rfolder" > android.files

ls -1 "$lfolder" > local.files

rm -f update.files
touch update.files

while IFS=  read -r q; do
  # Remove non-printable characters (are not visible on console)
  l=$(echo ${q} | sed 's/[^[:print:]]//')
  # Populate files to update
  if ! grep -q "$l" local.files; then         
    echo "$l" >> update.files
  fi  
done < android.files

script_dir=$(pwd)
cd $lfolder

while IFS=  read -r q; do
  # Remove non-printable characters (are not visible on console)
  l=$(echo ${q} | sed 's/[^[:print:]]//')
  echo "Get file: $l"
  adb pull "$rfolder/$l"
done < "${script_dir}"/update.files
