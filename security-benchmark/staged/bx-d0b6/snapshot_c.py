def execute(data):
    accepted = not data["child_time"] < data["parent_time"]
    return {"accepted": accepted}
