def execute(data):
    accepted = data["child_time"] >= data["parent_time"]
    return {"accepted": accepted}
